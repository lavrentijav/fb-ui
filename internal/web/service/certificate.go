package service

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certissuer"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// certRoot is where issued certificates are written: the same
// <root>/<domain>/{fullchain,privkey}.pem layout x-ui.sh and acme.sh use.
var certRoot = "/root/cert"

// nodeCertDir keeps this panel's copy of a node certificate out of certRoot's
// top level, where Scan and x-ui.sh expect this host's own certificates.
func nodeCertDir(domain string) string {
	return filepath.Join(certRoot, ".nodes", strings.TrimPrefix(domain, "*."))
}

// issueCertificate is the ACME call; tests replace it with a local CA.
var issueCertificate = certissuer.Issue

const (
	certRenewBefore  = 30 * 24 * time.Hour
	certWarnBefore   = 14 * 24 * time.Hour
	certRetryBackoff = 6 * time.Hour
	certIssueTimeout = 10 * time.Minute
)

// CertificateService issues, renews and watches TLS certificates.
type CertificateService struct {
	dns  DnsProviderService
	xray XrayService
}

// CertificateRequest is the add/update payload. DnsProviderId 0 on a dns01
// certificate means "pick the provider whose zone owns the first domain".
type CertificateRequest struct {
	Domains       []string `json:"domains" form:"domains" example:"[\"msk1.corp.example\"]"`
	Challenge     string   `json:"challenge" form:"challenge" example:"dns01"`
	DnsProviderId int      `json:"dnsProviderId" form:"dnsProviderId" example:"1"`
	AutoRenew     *bool    `json:"autoRenew" form:"autoRenew" example:"true"`
	NodeId        int      `json:"nodeId" form:"nodeId" example:"0"`
}

// CertInstallRequest is a certificate a master issued for this panel.
type CertInstallRequest struct {
	Domain  string `json:"domain" form:"domain" example:"msk1.corp.example"`
	CertPem string `json:"certPem" form:"certPem" example:"-----BEGIN CERTIFICATE-----"`
	KeyPem  string `json:"keyPem" form:"keyPem" example:"-----BEGIN EC PRIVATE KEY-----"`
}

// AcmeAccountView is the account the panel issues under; the key stays private.
type AcmeAccountView struct {
	Email      string `json:"email" example:"ops@example.com"`
	Directory  string `json:"directory" example:"https://acme-v02.api.letsencrypt.org/directory"`
	Registered bool   `json:"registered" example:"true"`
}

// AcmeAccountRequest changes the contact email or the CA directory.
type AcmeAccountRequest struct {
	Email     string `json:"email" form:"email" example:"ops@example.com"`
	Directory string `json:"directory" form:"directory" example:"https://acme-v02.api.letsencrypt.org/directory"`
}

var certLocks sync.Map // cert id -> *sync.Mutex; one issuance per cert at a time

func certLock(id int) *sync.Mutex {
	m, _ := certLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (s *CertificateService) List() ([]*model.Certificate, error) {
	var rows []*model.Certificate
	err := database.GetDB().Order("not_after asc, id asc").Find(&rows).Error
	return rows, err
}

func (s *CertificateService) GetById(id int) (*model.Certificate, error) {
	row := &model.Certificate{}
	if err := database.GetDB().Where("id = ?", id).First(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func normalizeDomains(in []string) ([]string, error) {
	var out []string
	for _, d := range in {
		for part := range strings.FieldsFuncSeq(d, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
			n := dnsprovider.NormalizeName(part)
			if n == "" || slices.Contains(out, n) {
				continue
			}
			if strings.ContainsAny(n, "/:@") || !strings.Contains(n, ".") || strings.Contains(strings.TrimPrefix(n, "*."), "*") {
				return nil, common.NewErrorf("%q is not a valid certificate domain", part)
			}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, common.NewError("at least one domain is required")
	}
	return out, nil
}

func (s *CertificateService) fromRequest(dst *model.Certificate, req *CertificateRequest) error {
	domains, err := normalizeDomains(req.Domains)
	if err != nil {
		return err
	}
	dst.Domains = domains
	dst.Challenge = strings.TrimSpace(req.Challenge)
	if dst.Challenge == "" {
		dst.Challenge = model.CertChallengeDNS01
	}
	if req.AutoRenew != nil {
		dst.AutoRenew = *req.AutoRenew
	}
	switch dst.Challenge {
	case model.CertChallengeDNS01:
		dst.DnsProviderId = req.DnsProviderId
		first := strings.TrimPrefix(domains[0], "*.")
		zone := ""
		if dst.DnsProviderId == 0 {
			var prov *model.DnsProvider
			if prov, zone, err = s.dns.ForName(first); err != nil {
				return common.NewErrorf("add a DNS provider for %s first, or use http01", domains[0])
			}
			dst.DnsProviderId = prov.Id
		} else if _, zone, err = s.dns.ClientFor(dst.DnsProviderId, first); err != nil {
			return common.NewErrorf("DNS provider %d cannot write %s: %v", dst.DnsProviderId, first, err)
		}
		for _, d := range domains {
			if !dnsprovider.InZone(strings.TrimPrefix(d, "*."), zone) {
				return common.NewErrorf("%s is outside zone %s; issue it separately", d, zone)
			}
		}
	case model.CertChallengeHTTP01:
		if req.NodeId > 0 {
			return common.NewError("a node's certificate is ordered here, so it needs dns01")
		}
		dst.DnsProviderId = 0
		for _, d := range domains {
			if strings.HasPrefix(d, "*.") {
				return common.NewErrorf("wildcard %s needs dns01", d)
			}
		}
	default:
		return common.NewErrorf("challenge %q cannot be issued; use dns01 or http01", dst.Challenge)
	}
	if req.NodeId > 0 && dst.NodeId != req.NodeId {
		if _, err := (&NodeService{}).GetById(req.NodeId); err != nil {
			return common.NewErrorf("node %d not found", req.NodeId)
		}
		dst.NodeId, dst.PushedAt = req.NodeId, 0
	}
	if dst.CertFile == "" {
		dir := filepath.Join(certRoot, strings.TrimPrefix(domains[0], "*."))
		if dst.NodeId > 0 {
			dir = nodeCertDir(domains[0])
		}
		dst.CertFile = filepath.Join(dir, "fullchain.pem")
		dst.KeyFile = filepath.Join(dir, "privkey.pem")
	}
	return nil
}

func (s *CertificateService) Create(req *CertificateRequest) (*model.Certificate, error) {
	row := &model.Certificate{AutoRenew: true, Status: model.CertStatusPending}
	if err := s.fromRequest(row, req); err != nil {
		return nil, err
	}
	var clash int64
	if err := database.GetDB().Model(&model.Certificate{}).Where("cert_file = ?", row.CertFile).Count(&clash).Error; err != nil {
		return nil, err
	}
	if clash > 0 {
		return nil, common.NewErrorf("a certificate already writes %s", row.CertFile)
	}
	if err := database.GetDB().Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (s *CertificateService) Update(id int, req *CertificateRequest) error {
	row, err := s.GetById(id)
	if err != nil {
		return err
	}
	if row.Challenge == model.CertChallengeImported && (req.Challenge == "" || req.Challenge == model.CertChallengeImported) {
		if req.AutoRenew != nil {
			row.AutoRenew = *req.AutoRenew
		}
		return database.GetDB().Save(row).Error
	}
	if err := s.fromRequest(row, req); err != nil {
		return err
	}
	return database.GetDB().Save(row).Error
}

// Delete forgets a certificate; the PEM files stay on disk because Xray or
// the panel may still be pointed at them.
func (s *CertificateService) Delete(id int) error {
	return database.GetDB().Where("id = ?", id).Delete(&model.Certificate{}).Error
}

// StartIssue marks the certificate as issuing and runs the order in the
// background; the UI polls the list for the outcome.
func (s *CertificateService) StartIssue(id int) error {
	row, err := s.GetById(id)
	if err != nil {
		return err
	}
	if row.Challenge == model.CertChallengeImported {
		return common.NewError("an imported certificate is renewed by whatever issued it")
	}
	if err := s.setStatus(id, model.CertStatusIssuing, ""); err != nil {
		return err
	}
	go func() {
		if err := s.Issue(id); err != nil {
			logger.Warning("certificate", id, "issue failed:", err)
		}
	}()
	return nil
}

func (s *CertificateService) setStatus(id int, status, lastErr string) error {
	return database.GetDB().Model(&model.Certificate{}).Where("id = ?", id).Updates(map[string]any{
		"status":          status,
		"last_error":      lastErr,
		"last_attempt_at": time.Now().Unix(),
	}).Error
}

// Issue runs one ACME order for the certificate and installs the result.
func (s *CertificateService) Issue(id int) error {
	mu := certLock(id)
	mu.Lock()
	defer mu.Unlock()

	row, err := s.GetById(id)
	if err != nil {
		return err
	}
	if err := s.issue(row); err != nil {
		_ = s.setStatus(id, model.CertStatusError, err.Error())
		return err
	}
	return nil
}

func (s *CertificateService) issue(row *model.Certificate) error {
	acct, key, err := s.ensureAccount()
	if err != nil {
		return err
	}
	req := certissuer.Request{
		DirectoryURL: acct.Directory,
		AccountKey:   key,
		Email:        acct.Email,
		Domains:      slices.Clone(row.Domains),
		Challenge:    row.Challenge,
		Log: func(format string, args ...any) {
			logger.Infof("certificate %d: "+format, append([]any{row.Id}, args...)...)
		},
	}
	if row.Challenge == model.CertChallengeDNS01 {
		if req.DNS, req.Zone, err = s.dns.ClientFor(row.DnsProviderId, strings.TrimPrefix(row.Domains[0], "*.")); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), certIssueTimeout)
	defer cancel()
	res, err := issueCertificate(ctx, req)
	if err != nil {
		return err
	}
	if acct.Uri == "" && res.AccountURI != "" {
		_ = database.GetDB().Model(acct).Update("uri", res.AccountURI).Error
	}
	if err := writePEMPair(row.CertFile, row.KeyFile, res.CertPEM, res.KeyPEM); err != nil {
		return err
	}
	if err := database.GetDB().Model(&model.Certificate{}).Where("id = ?", row.Id).Updates(map[string]any{
		"issuer":          res.Issuer,
		"not_before":      res.NotBefore.Unix(),
		"not_after":       res.NotAfter.Unix(),
		"status":          model.CertStatusValid,
		"last_error":      "",
		"last_attempt_at": time.Now().Unix(),
	}).Error; err != nil {
		return err
	}
	if row.NodeId > 0 {
		// The order succeeded; a node that is down gets it on the next renew tick.
		if err := s.pushToNode(row.Id); err != nil {
			logger.Warning("certificate", row.Id, err)
		}
		return nil
	}
	s.reloadConsumers(row.CertFile)
	return nil
}

// certPusher sends a pair to a node; tests replace it.
var certPusher = func(n *model.Node, req *CertInstallRequest) error {
	mgr := runtime.GetManager()
	if mgr == nil {
		return errors.New("runtime manager unavailable")
	}
	remote, err := mgr.RemoteFor(n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return remote.InstallCert(ctx, req.Domain, req.CertPem, req.KeyPem)
}

// pushToNode installs the stored pair on the certificate's node and records
// the outcome; a failure is retried by the renew job.
func (s *CertificateService) pushToNode(id int) error {
	row, err := s.GetById(id)
	if err != nil {
		return err
	}
	pushErr := s.push(row)
	updates := map[string]any{"push_error": ""}
	if pushErr != nil {
		updates["push_error"] = pushErr.Error()
	} else {
		updates["pushed_at"] = time.Now().Unix()
	}
	if err := database.GetDB().Model(&model.Certificate{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	if pushErr != nil {
		return fmt.Errorf("push to node %d: %w", row.NodeId, pushErr)
	}
	return nil
}

func (s *CertificateService) push(row *model.Certificate) error {
	n, err := (&NodeService{}).GetById(row.NodeId)
	if err != nil || n == nil {
		return fmt.Errorf("node %d not found", row.NodeId)
	}
	certPEM, err := os.ReadFile(row.CertFile)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(row.KeyFile)
	if err != nil {
		return err
	}
	return certPusher(n, &CertInstallRequest{
		Domain: strings.TrimPrefix(row.Domains[0], "*."), CertPem: string(certPEM), KeyPem: string(keyPEM),
	})
}

// Install stores a pair a master issued for this panel under certRoot and
// watches it as imported; the master stays the one that renews it.
func (s *CertificateService) Install(req *CertInstallRequest) error {
	domain := dnsprovider.NormalizeName(req.Domain)
	if domain == "" || strings.ContainsAny(domain, "/\\ ") || strings.HasPrefix(domain, ".") {
		return common.NewErrorf("%q is not a domain", req.Domain)
	}
	pair, err := tls.X509KeyPair([]byte(req.CertPem), []byte(req.KeyPem))
	if err != nil {
		return common.NewErrorf("certificate and key do not form a pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		return common.NewErrorf("certificate does not cover %s", domain)
	}
	dir := filepath.Join(certRoot, domain)
	certFile, keyFile := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	if err := writePEMPair(certFile, keyFile, []byte(req.CertPem), []byte(req.KeyPem)); err != nil {
		return err
	}
	row := &model.Certificate{}
	err = database.GetDB().Where("cert_file = ?", certFile).First(row).Error
	if err != nil && !database.IsNotFound(err) {
		return err
	}
	row.Domains = leafDomains(leaf, domain)
	row.Challenge, row.AutoRenew, row.Status, row.LastError = model.CertChallengeImported, false, model.CertStatusValid, ""
	row.CertFile, row.KeyFile = certFile, keyFile
	applyLeaf(row, leaf)
	if err := database.GetDB().Save(row).Error; err != nil {
		return err
	}
	s.reloadConsumers(certFile)
	return nil
}

// writePEMPair replaces both files via rename so a reader never sees a new
// chain paired with the old key.
func writePEMPair(certFile, keyFile string, certPEM, keyPEM []byte) error {
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{keyFile, keyPEM, 0o600}, {certFile, certPEM, 0o644}} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		tmp := f.path + ".tmp"
		if err := os.WriteFile(tmp, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, f.path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

// reloadConsumers restarts local Xray when an inbound reads the renewed file.
// The panel and subscription listeners re-read theirs on the next handshake.
func (s *CertificateService) reloadConsumers(certFile string) {
	var n int64
	err := database.GetDB().Model(&model.Inbound{}).
		Where("(node_id IS NULL OR node_id = 0) AND enable = ? AND stream_settings LIKE ?", true, "%"+certFile+"%").
		Count(&n).Error
	if err == nil && n > 0 {
		s.xray.SetToNeedRestart()
	}
}

func (s *CertificateService) ensureAccount() (*model.AcmeAccount, crypto.Signer, error) {
	acct := &model.AcmeAccount{}
	err := database.GetDB().Order("id asc").First(acct).Error
	if database.IsNotFound(err) {
		acct = &model.AcmeAccount{Directory: certissuer.LetsEncryptDirectory}
	} else if err != nil {
		return nil, nil, err
	}
	if acct.KeyPem == "" {
		_, pemKey, err := certissuer.NewAccountKey()
		if err != nil {
			return nil, nil, err
		}
		acct.KeyPem = string(pemKey)
		acct.Uri = ""
		if err := database.GetDB().Save(acct).Error; err != nil {
			return nil, nil, err
		}
	}
	key, err := certissuer.ParseAccountKey([]byte(acct.KeyPem))
	if err != nil {
		return nil, nil, err
	}
	return acct, key, nil
}

func (s *CertificateService) GetAccount() (*AcmeAccountView, error) {
	acct := &model.AcmeAccount{}
	err := database.GetDB().Order("id asc").First(acct).Error
	if database.IsNotFound(err) {
		return &AcmeAccountView{Directory: certissuer.LetsEncryptDirectory}, nil
	}
	if err != nil {
		return nil, err
	}
	return &AcmeAccountView{Email: acct.Email, Directory: acct.Directory, Registered: acct.Uri != ""}, nil
}

// SetAccount stores the contact email; switching CA directory drops the key
// so the next issuance registers afresh at the new CA.
func (s *CertificateService) SetAccount(req *AcmeAccountRequest) error {
	email := strings.TrimSpace(req.Email)
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \t<>")) {
		return common.NewErrorf("%q is not an email address", email)
	}
	dir := strings.TrimSpace(req.Directory)
	if dir == "" {
		dir = certissuer.LetsEncryptDirectory
	}
	if !strings.HasPrefix(dir, "https://") {
		return common.NewError("the ACME directory must be an https:// URL")
	}
	acct := &model.AcmeAccount{}
	err := database.GetDB().Order("id asc").First(acct).Error
	if err != nil && !database.IsNotFound(err) {
		return err
	}
	if acct.Directory != dir || acct.Email != email {
		acct.KeyPem, acct.Uri = "", ""
	}
	acct.Directory, acct.Email = dir, email
	return database.GetDB().Save(acct).Error
}

// Scan imports certificates already on disk under certRoot (acme.sh, x-ui.sh,
// manual) so their expiry is watched. It returns how many rows were added.
func (s *CertificateService) Scan() (int, error) {
	matches, err := filepath.Glob(filepath.Join(certRoot, "*", "fullchain.pem"))
	if err != nil {
		return 0, err
	}
	added := 0
	for _, certFile := range matches {
		var n int64
		if err := database.GetDB().Model(&model.Certificate{}).Where("cert_file = ?", certFile).Count(&n).Error; err != nil {
			return added, err
		}
		if n > 0 {
			continue
		}
		leaf, err := readLeaf(certFile)
		if err != nil {
			logger.Warning("certificate scan: skip", certFile+":", err)
			continue
		}
		keyFile := filepath.Join(filepath.Dir(certFile), "privkey.pem")
		row := &model.Certificate{
			Domains:   leafDomains(leaf, filepath.Base(filepath.Dir(certFile))),
			Challenge: model.CertChallengeImported,
			CertFile:  certFile,
			KeyFile:   keyFile,
			AutoRenew: false,
			Status:    model.CertStatusValid,
		}
		applyLeaf(row, leaf)
		if err := database.GetDB().Create(row).Error; err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

func readLeaf(certFile string) (*x509.Certificate, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

func leafDomains(leaf *x509.Certificate, fallback string) []string {
	if len(leaf.DNSNames) > 0 {
		return slices.Clone(leaf.DNSNames)
	}
	if leaf.Subject.CommonName != "" {
		return []string{leaf.Subject.CommonName}
	}
	return []string{fallback}
}

func applyLeaf(row *model.Certificate, leaf *x509.Certificate) {
	row.NotBefore, row.NotAfter = leaf.NotBefore.Unix(), leaf.NotAfter.Unix()
	row.Issuer = leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		row.Issuer = strings.TrimSpace(leaf.Issuer.Organization[0] + " " + row.Issuer)
	}
}

// RenewDue is the daily job body: renew what is close to expiry, refresh the
// dates of imported certificates from disk and warn about the rest.
func (s *CertificateService) RenewDue(now time.Time) {
	rows, err := s.List()
	if err != nil {
		logger.Warning("certificate renew: list failed:", err)
		return
	}
	for _, row := range rows {
		if row.Challenge == model.CertChallengeImported {
			if leaf, err := readLeaf(row.CertFile); err == nil && leaf.NotAfter.Unix() != row.NotAfter {
				applyLeaf(row, leaf)
				_ = database.GetDB().Model(row).Updates(map[string]any{
					"not_before": row.NotBefore, "not_after": row.NotAfter, "issuer": row.Issuer,
				}).Error
			}
		} else if row.AutoRenew && certNeedsRenewal(row, now) {
			if err := s.Issue(row.Id); err != nil {
				publishCertEvent(eventbus.EventCertRenewFails, row, err.Error())
				continue
			}
			if row, err = s.GetById(row.Id); err != nil {
				continue
			}
		}
		if row.NodeId > 0 && row.Status == model.CertStatusValid && (row.PushedAt == 0 || row.PushError != "") {
			if err := s.pushToNode(row.Id); err != nil {
				logger.Warning("certificate", row.Id, err)
			}
		}
		if row.NotAfter > 0 && time.Unix(row.NotAfter, 0).Sub(now) < certWarnBefore {
			publishCertEvent(eventbus.EventCertExpiring, row, "")
		}
	}
}

func certNeedsRenewal(row *model.Certificate, now time.Time) bool {
	if row.Status == model.CertStatusError && now.Unix()-row.LastAttemptAt < int64(certRetryBackoff/time.Second) {
		return false
	}
	return row.NotAfter == 0 || time.Unix(row.NotAfter, 0).Sub(now) < certRenewBefore
}

func publishCertEvent(t eventbus.EventType, row *model.Certificate, errMsg string) {
	if eventBus == nil {
		return
	}
	eventBus.Publish(eventbus.Event{
		Type:      t,
		Source:    strings.Join(row.Domains, ", "),
		Timestamp: time.Now(),
		Data: &eventbus.CertEventData{
			CertId:   row.Id,
			Domains:  slices.Clone(row.Domains),
			NotAfter: time.Unix(row.NotAfter, 0),
			Error:    errMsg,
		},
	})
}

// ApplyToPanel points the panel ("web") or subscription ("sub") listener at
// the certificate. It takes effect on the next panel restart.
func (s *CertificateService) ApplyToPanel(id int, target string) error {
	row, err := s.GetById(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(row.CertFile); err != nil {
		return common.NewErrorf("issue the certificate first: %v", err)
	}
	var settings SettingService
	switch target {
	case "web":
		if err := settings.SetCertFile(row.CertFile); err != nil {
			return err
		}
		return settings.SetKeyFile(row.KeyFile)
	case "sub":
		if err := settings.SetSubCertFile(row.CertFile); err != nil {
			return err
		}
		return settings.SetSubKeyFile(row.KeyFile)
	default:
		return fmt.Errorf("unknown target %q (want web or sub)", target)
	}
}
