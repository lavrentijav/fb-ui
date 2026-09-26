package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certissuer"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

func initCertTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	oldRoot := certRoot
	certRoot = filepath.Join(dir, "cert")
	t.Cleanup(func() { certRoot = oldRoot })
}

// selfSigned is a stand-in CA: it signs for the requested names with the
// given validity so renewal windows can be exercised without a network.
func selfSigned(t *testing.T, domains []string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: domains[0]},
		Issuer:       pkix.Name{CommonName: "Test CA"},
		DNSNames:     domains,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

type fakeCA struct {
	mu       sync.Mutex
	validFor time.Duration
	fail     error
	requests []certissuer.Request
}

func (f *fakeCA) issue(t *testing.T) func(context.Context, certissuer.Request) (*certissuer.Result, error) {
	return func(_ context.Context, req certissuer.Request) (*certissuer.Result, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, req)
		if f.fail != nil {
			return nil, f.fail
		}
		notAfter := time.Now().Add(f.validFor).Truncate(time.Second)
		c, k := selfSigned(t, req.Domains, notAfter)
		return &certissuer.Result{CertPEM: c, KeyPEM: k, NotBefore: time.Now(), NotAfter: notAfter, Issuer: "Test CA", AccountURI: "acct/1"}, nil
	}
}

func installFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	ca := &fakeCA{validFor: 90 * 24 * time.Hour}
	old := issueCertificate
	issueCertificate = ca.issue(t)
	t.Cleanup(func() { issueCertificate = old })
	oldFactory := dnsProviderFactory
	dnsProviderFactory = func(cfg dnsprovider.Config) (dnsprovider.Provider, error) {
		return nopDNS{}, nil
	}
	t.Cleanup(func() { dnsProviderFactory = oldFactory })
	return ca
}

type nopDNS struct{}

func (nopDNS) SetRecords(context.Context, string, string, []string, int) error { return nil }
func (nopDNS) DeleteRecords(context.Context, string, string) error             { return nil }
func (nopDNS) Check(context.Context) error                                     { return nil }

func addZone(t *testing.T, zone string) *model.DnsProvider {
	t.Helper()
	var dns DnsProviderService
	p, err := dns.Create(&DnsProviderRequest{Type: model.DnsProviderCloudflare, Zone: zone, Credential: "secret-" + zone})
	if err != nil {
		t.Fatalf("create zone %s: %v", zone, err)
	}
	return p
}

func TestCertificateCreatePicksOwningZone(t *testing.T) {
	initCertTestDB(t)
	installFakeCA(t)
	parent := addZone(t, "example.com")
	child := addZone(t, "ru.example.com")
	var s CertificateService

	c, err := s.Create(&CertificateRequest{Domains: []string{"MSK1.ru.example.com.", "*.msk1.ru.example.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.DnsProviderId != child.Id {
		t.Fatalf("provider = %d, want the delegated sub-zone %d (not parent %d)", c.DnsProviderId, child.Id, parent.Id)
	}
	if !slices.Equal(c.Domains, []string{"msk1.ru.example.com", "*.msk1.ru.example.com"}) {
		t.Fatalf("domains = %v", c.Domains)
	}
	if want := filepath.Join(certRoot, "msk1.ru.example.com", "fullchain.pem"); c.CertFile != want {
		t.Fatalf("CertFile = %q, want %q", c.CertFile, want)
	}

	cases := []struct {
		name string
		req  CertificateRequest
		want string
	}{
		{"domain outside the chosen zone", CertificateRequest{Domains: []string{"a.ru.example.com", "b.example.com"}}, "b.example.com is outside zone ru.example.com"},
		{"no zone at all", CertificateRequest{Domains: []string{"x.other.org"}}, "add a DNS provider for x.other.org first"},
		{"wildcard over http01", CertificateRequest{Domains: []string{"*.example.com"}, Challenge: "http01"}, "wildcard *.example.com needs dns01"},
		{"same files twice", CertificateRequest{Domains: []string{"msk1.ru.example.com"}}, "already writes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(&tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCertificateIssueInstallsFilesAndRecordsFailure(t *testing.T) {
	initCertTestDB(t)
	ca := installFakeCA(t)
	addZone(t, "example.com")
	var s CertificateService
	c, err := s.Create(&CertificateRequest{Domains: []string{"node.example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Issue(c.Id); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, _ := s.GetById(c.Id)
	if got.Status != model.CertStatusValid || got.Issuer != "Test CA" || got.NotAfter == 0 {
		t.Fatalf("after issue: %+v", got)
	}
	if st, err := os.Stat(got.KeyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v mode %v, want 0600", err, st)
	}
	if req := ca.requests[0]; req.Challenge != certissuer.DNS01 || req.Zone != "example.com" || req.DNS == nil {
		t.Fatalf("issuer got challenge=%q zone=%q dns=%v", req.Challenge, req.Zone, req.DNS)
	}

	ca.fail = errors.New("rateLimited: too many certificates")
	if err := s.Issue(c.Id); err == nil {
		t.Fatal("Issue should surface the CA error")
	}
	got, _ = s.GetById(c.Id)
	if got.Status != model.CertStatusError || !strings.Contains(got.LastError, "rateLimited") {
		t.Fatalf("after failure: status=%q lastError=%q", got.Status, got.LastError)
	}
	if _, err := os.Stat(got.CertFile); err != nil {
		t.Fatalf("a failed renewal must keep the previous files: %v", err)
	}
}

func TestCertificateRenewDue(t *testing.T) {
	initCertTestDB(t)
	ca := installFakeCA(t)
	addZone(t, "example.com")
	bus := eventbus.New(16)
	var mu sync.Mutex
	var events []eventbus.EventType
	bus.Subscribe("test", func(e eventbus.Event) {
		mu.Lock()
		events = append(events, e.Type)
		mu.Unlock()
	})
	SetEventBus(bus)
	t.Cleanup(func() { SetEventBus(nil) })
	var s CertificateService

	fresh, _ := s.Create(&CertificateRequest{Domains: []string{"fresh.example.com"}})
	due, _ := s.Create(&CertificateRequest{Domains: []string{"due.example.com"}})
	manual, _ := s.Create(&CertificateRequest{Domains: []string{"manual.example.com"}, AutoRenew: new(false)})
	now := time.Now()
	database.GetDB().Model(&model.Certificate{}).Where("id = ?", fresh.Id).Updates(map[string]any{"not_after": now.Add(60 * 24 * time.Hour).Unix(), "status": "valid"})
	database.GetDB().Model(&model.Certificate{}).Where("id = ?", due.Id).Updates(map[string]any{"not_after": now.Add(10 * 24 * time.Hour).Unix(), "status": "valid"})
	database.GetDB().Model(&model.Certificate{}).Where("id = ?", manual.Id).Updates(map[string]any{"not_after": now.Add(5 * 24 * time.Hour).Unix(), "status": "valid"})

	s.RenewDue(now)
	if len(ca.requests) != 1 || ca.requests[0].Domains[0] != "due.example.com" {
		t.Fatalf("renewed %d certs (%v), want only due.example.com", len(ca.requests), ca.requests)
	}
	if got, _ := s.GetById(due.Id); time.Unix(got.NotAfter, 0).Before(now.Add(80 * 24 * time.Hour)) {
		t.Fatalf("due cert not extended: notAfter=%v", time.Unix(got.NotAfter, 0))
	}

	ca.fail = errors.New("dns down")
	database.GetDB().Model(&model.Certificate{}).Where("id = ?", due.Id).Update("not_after", now.Add(10*24*time.Hour).Unix())
	s.RenewDue(now)
	s.RenewDue(now.Add(time.Hour))
	if len(ca.requests) != 2 {
		t.Fatalf("CA called %d times, want 2: a failed renewal must back off before retrying", len(ca.requests))
	}
	s.RenewDue(now.Add(certRetryBackoff + time.Minute))
	if len(ca.requests) != 3 {
		t.Fatalf("CA called %d times, want 3 once the backoff passed", len(ca.requests))
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := slices.Index(events, eventbus.EventCertRenewFails)
		m := slices.Index(events, eventbus.EventCertExpiring)
		mu.Unlock()
		if n >= 0 && m >= 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %v, want renew_failed and expiring", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCertificateScanImportsOnce(t *testing.T) {
	initCertTestDB(t)
	dir := filepath.Join(certRoot, "old.example.com")
	_ = os.MkdirAll(dir, 0o755)
	notAfter := time.Now().Add(40 * 24 * time.Hour).Truncate(time.Second)
	c, k := selfSigned(t, []string{"old.example.com", "www.old.example.com"}, notAfter)
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), c, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), k, 0o600)
	var s CertificateService

	for i, want := range []int{1, 0} {
		n, err := s.Scan()
		if err != nil || n != want {
			t.Fatalf("scan #%d: added %d err %v, want %d", i+1, n, err, want)
		}
	}
	rows, _ := s.List()
	if len(rows) != 1 || rows[0].Challenge != model.CertChallengeImported || rows[0].NotAfter != notAfter.Unix() ||
		!slices.Equal(rows[0].Domains, []string{"old.example.com", "www.old.example.com"}) {
		t.Fatalf("imported row = %+v", rows[0])
	}
	if err := s.StartIssue(rows[0].Id); err == nil {
		t.Fatal("an imported certificate must not be issued by the panel")
	}
}

func TestDnsProviderCredentialIsWriteOnly(t *testing.T) {
	initCertTestDB(t)
	installFakeCA(t)
	var dns DnsProviderService
	p := addZone(t, "example.com")
	if p.HasCredential != true {
		t.Fatalf("created view: %+v", p)
	}
	if err := dns.Update(p.Id, &DnsProviderRequest{Type: model.DnsProviderCloudflare, Zone: "example.com", Name: "renamed"}); err != nil {
		t.Fatalf("update without credential: %v", err)
	}
	stored, _ := dns.load(p.Id)
	if stored.Credential != "secret-example.com" || stored.Name != "renamed" {
		t.Fatalf("stored = name %q cred %q; an empty credential must keep the old one", stored.Name, stored.Credential)
	}
	list, _ := dns.List()
	if list[0].Credential != "" || !list[0].HasCredential {
		t.Fatalf("list leaks or loses credential flag: %+v", list[0])
	}
	if _, err := dns.Create(&DnsProviderRequest{Type: model.DnsProviderTechnitium, Zone: "home.example", Credential: "t"}); err == nil ||
		!strings.Contains(err.Error(), "Technitium needs its API URL") {
		t.Fatalf("technitium without URL: %v", err)
	}

	var certs CertificateService
	if _, err := certs.Create(&CertificateRequest{Domains: []string{"a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := dns.Delete(p.Id); err == nil || !strings.Contains(err.Error(), "1 certificate(s)") {
		t.Fatalf("deleting a provider in use: %v", err)
	}
}

func TestDnsAccountDiscoversZonesAndPicksTheOwner(t *testing.T) {
	initCertTestDB(t)
	installFakeCA(t)
	served := []string{"home.example", "lab.home.example"}
	var seen dnsprovider.Config
	old := dnsZoneLister
	dnsZoneLister = func(_ context.Context, cfg dnsprovider.Config) ([]string, error) {
		seen = cfg
		return slices.Clone(served), nil
	}
	t.Cleanup(func() { dnsZoneLister = old })
	lastDnsResync.Store(0)

	var dns DnsProviderService
	acct, err := dns.Create(&DnsProviderRequest{Type: model.DnsProviderTechnitium, ApiUrl: "ns1.home.example", ApiPort: 5380, Credential: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if seen.BaseURL != "http://ns1.home.example:5380" || acct.Name != "ns1.home.example" || !slices.Equal(acct.Zones, served) {
		t.Fatalf("base=%q name=%q zones=%v", seen.BaseURL, acct.Name, acct.Zones)
	}
	if _, zone, err := dns.ForName("ru7.lab.home.example"); err != nil || zone != "lab.home.example" {
		t.Fatalf("ForName sub-zone: zone=%q err=%v", zone, err)
	}

	// A zone created in Technitium after the account was saved is found by
	// the lazy rediscovery, without anyone pressing "refresh".
	served = append(served, "corp.example")
	if _, zone, err := dns.ForName("msk1.corp.example"); err != nil || zone != "corp.example" {
		t.Fatalf("ForName after a new zone: zone=%q err=%v", zone, err)
	}
	if _, _, err := dns.ForName("x.other.org"); err == nil {
		t.Fatal("a name no account covers must fail")
	}

	served = nil
	if _, err := dns.Test(0, &DnsProviderRequest{Type: model.DnsProviderTechnitium, ApiUrl: "h", Credential: "t"}); err == nil ||
		!strings.Contains(err.Error(), "no writable (primary) zone") {
		t.Fatalf("token without zones: %v", err)
	}
}

func TestDnsApiBaseURL(t *testing.T) {
	cases := []struct {
		raw  string
		port int
		want string
	}{
		{"ns1.example.com", 0, "http://ns1.example.com:5380"},
		{"ns1.example.com", 5380, "http://ns1.example.com:5380"},
		{"ns1.example.com", 53443, "https://ns1.example.com:53443"},
		{"https://dns.example.com", 0, "https://dns.example.com"},
		{"https://dns.example.com:8443/", 9443, "https://dns.example.com:9443"},
		{"10.0.0.5", 5380, "http://10.0.0.5:5380"},
	}
	for _, tc := range cases {
		if got, err := dnsApiBaseURL(tc.raw, tc.port); err != nil || got != tc.want {
			t.Errorf("dnsApiBaseURL(%q, %d) = %q, %v; want %q", tc.raw, tc.port, got, err, tc.want)
		}
	}
	if _, err := dnsApiBaseURL("ftp://x", 0); err == nil {
		t.Error("ftp scheme must be rejected")
	}
}

func TestNodeCertificateIsPushedAndRetried(t *testing.T) {
	initCertTestDB(t)
	installFakeCA(t)
	addZone(t, "example.com")
	node := &model.Node{Name: "msk1", Scheme: "https", Address: "msk1.example.com", Port: 2053, Enable: true}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	var pushed []*CertInstallRequest
	var pushErr error
	old := certPusher
	certPusher = func(n *model.Node, req *CertInstallRequest) error {
		if n.Id != node.Id {
			t.Errorf("pushed to node %d, want %d", n.Id, node.Id)
		}
		pushed = append(pushed, req)
		return pushErr
	}
	t.Cleanup(func() { certPusher = old })
	var s CertificateService

	if _, err := s.Create(&CertificateRequest{Domains: []string{"msk1.example.com"}, Challenge: "http01", NodeId: node.Id}); err == nil {
		t.Fatal("a node certificate over http01 must be refused")
	}
	c, err := s.Create(&CertificateRequest{Domains: []string{"msk1.example.com"}, NodeId: node.Id})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(certRoot, ".nodes", "msk1.example.com", "fullchain.pem"); c.CertFile != want {
		t.Fatalf("CertFile = %q, want %q (outside the host's own cert root)", c.CertFile, want)
	}

	pushErr = errors.New("node offline")
	if err := s.Issue(c.Id); err != nil {
		t.Fatalf("an unreachable node must not fail the order: %v", err)
	}
	got, _ := s.GetById(c.Id)
	if got.Status != model.CertStatusValid || got.PushedAt != 0 || !strings.Contains(got.PushError, "node offline") {
		t.Fatalf("after failed push: status=%q pushedAt=%d pushError=%q", got.Status, got.PushedAt, got.PushError)
	}

	pushErr = nil
	s.RenewDue(time.Now())
	got, _ = s.GetById(c.Id)
	if len(pushed) != 2 || got.PushedAt == 0 || got.PushError != "" {
		t.Fatalf("renew tick: pushes=%d pushedAt=%d pushError=%q", len(pushed), got.PushedAt, got.PushError)
	}
	onDisk, _ := os.ReadFile(got.CertFile)
	if pushed[1].Domain != "msk1.example.com" || pushed[1].CertPem != string(onDisk) || !strings.Contains(pushed[1].KeyPem, "PRIVATE KEY") {
		t.Fatalf("pushed payload = %+v", pushed[1].Domain)
	}
	s.RenewDue(time.Now())
	if len(pushed) != 2 {
		t.Fatalf("a delivered certificate was pushed again (%d pushes)", len(pushed))
	}
}

func TestInstallStoresAMastersCertificate(t *testing.T) {
	initCertTestDB(t)
	var s CertificateService
	certPEM, keyPEM := selfSigned(t, []string{"msk1.example.com"}, time.Now().Add(90*24*time.Hour))
	otherCert, _ := selfSigned(t, []string{"msk1.example.com"}, time.Now().Add(90*24*time.Hour))

	bad := []struct {
		name string
		req  CertInstallRequest
		want string
	}{
		{"key of another cert", CertInstallRequest{Domain: "msk1.example.com", CertPem: string(otherCert), KeyPem: string(keyPEM)}, "do not form a pair"},
		{"wrong domain", CertInstallRequest{Domain: "spb1.example.com", CertPem: string(certPEM), KeyPem: string(keyPEM)}, "does not cover spb1.example.com"},
		{"path in domain", CertInstallRequest{Domain: "../etc", CertPem: string(certPEM), KeyPem: string(keyPEM)}, "is not a domain"},
	}
	for _, tc := range bad {
		if err := s.Install(&tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}

	req := &CertInstallRequest{Domain: "MSK1.example.com", CertPem: string(certPEM), KeyPem: string(keyPEM)}
	if err := s.Install(req); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(req); err != nil {
		t.Fatalf("a renewal push must replace, not clash: %v", err)
	}
	rows, _ := s.List()
	if len(rows) != 1 || rows[0].Challenge != model.CertChallengeImported || rows[0].AutoRenew || rows[0].NotAfter == 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if data, _ := os.ReadFile(filepath.Join(certRoot, "msk1.example.com", "privkey.pem")); string(data) != string(keyPEM) {
		t.Fatal("key not written under the cert root")
	}
}
