package service

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/provision"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// NodeProvisionRequest is "here is a fresh server, make it a node": an address,
// root credentials and the name it should get. The SSH secrets are used for
// this one job and never stored.
type NodeProvisionRequest struct {
	Name          string `json:"name" form:"name" example:"msk2"`
	Host          string `json:"host" form:"host" example:"203.0.113.10"`
	SshPort       int    `json:"sshPort" form:"sshPort" example:"22"`
	SshUser       string `json:"sshUser" form:"sshUser" example:"root"`
	SshPassword   string `json:"sshPassword" form:"sshPassword" example:"root-password"`
	SshPrivateKey string `json:"sshPrivateKey" form:"sshPrivateKey" example:""`
	HostKeySha256 string `json:"hostKeySha256" form:"hostKeySha256" example:""`

	// Domain is the node's FQDN. Empty with a DnsProviderId means
	// <name>.<zone>; empty without one installs on the bare IP.
	Domain        string `json:"domain" form:"domain" example:"msk2.fire-core.ru"`
	DnsProviderId int    `json:"dnsProviderId" form:"dnsProviderId" example:"1"`
	SkipDns       bool   `json:"skipDns" form:"skipDns" example:"false"`
	AcmeEmail     string `json:"acmeEmail" form:"acmeEmail" example:"ops@example.com"`
	PanelPort     int    `json:"panelPort" form:"panelPort" example:"0"`

	// InstallSource "self" ships this panel's own build (the node never needs
	// GitHub); "github" lets install.sh download the latest release.
	InstallSource string `json:"installSource" form:"installSource" example:"self"`

	AuthorizedKey       string `json:"authorizedKey" form:"authorizedKey" example:"ssh-ed25519 AAAA... ops"`
	DisablePasswordAuth bool   `json:"disablePasswordAuth" form:"disablePasswordAuth" example:"false"`
	Force               bool   `json:"force" form:"force" example:"false"`
	// AllowPrivateAddress lets the node sit on a private network with this
	// panel, the same opt-in the Nodes form has.
	AllowPrivateAddress bool `json:"allowPrivateAddress" form:"allowPrivateAddress" example:"false"`
}

// Provision step names, in run order.
const (
	stepPreflight = "preflight"
	stepDNS       = "dns"
	stepUpload    = "upload"
	stepInstall   = "install"
	stepRegister  = "register"
	stepVerify    = "verify"
	stepHarden    = "harden"
)

// ProvisionStep is one stage of a job as the UI shows it.
type ProvisionStep struct {
	Name       string `json:"name" example:"install"`
	Status     string `json:"status" example:"done"` // pending|running|done|skipped|failed
	Detail     string `json:"detail" example:"installed from this panel's build"`
	StartedAt  int64  `json:"startedAt" example:"1700000000"`
	FinishedAt int64  `json:"finishedAt" example:"1700000060"`
}

// ProvisionJob is the observable state of one provisioning run.
type ProvisionJob struct {
	Id        string          `json:"id" example:"p-1700000000-1"`
	Name      string          `json:"name" example:"msk2"`
	Host      string          `json:"host" example:"203.0.113.10"`
	Domain    string          `json:"domain" example:"msk2.fire-core.ru"`
	Status    string          `json:"status" example:"running"` // running|done|failed
	Error     string          `json:"error" example:""`
	HostKey   string          `json:"hostKey" example:"SHA256:abc"`
	NodeId    int             `json:"nodeId" example:"3"`
	PanelUrl  string          `json:"panelUrl" example:"https://msk2.fire-core.ru:24501/abc/"`
	Steps     []ProvisionStep `json:"steps"`
	Log       []string        `json:"log" example:"[\"== preflight\"]"`
	CreatedAt int64           `json:"createdAt" example:"1700000000"`
}

// ReleaseAssets are the installer files the binary embeds (set from main), so
// provisioning works from any copy of the panel, not just a release checkout.
type ReleaseAssets struct {
	InstallScript []byte
	Files         map[string][]byte
}

var (
	releaseAssets   ReleaseAssets
	provisionJobs   = &jobStore{jobs: map[string]*provisionRun{}}
	provisionDialer = provision.Dial
)

const (
	provisionTimeout  = 30 * time.Minute
	provisionLogLines = 3000
	provisionKeepJobs = 50
	remoteWorkDir     = "/root/.fbui-provision"
)

var provisionRetryDelay = 5 * time.Second

func SetReleaseAssets(a ReleaseAssets) { releaseAssets = a }

// NodeProvisionService turns a bare server into a registered node.
type NodeProvisionService struct {
	nodes NodeService
	dns   DnsProviderService
}

type provisionRun struct {
	mu      sync.Mutex
	job     ProvisionJob
	secrets []string
}

type jobStore struct {
	mu    sync.Mutex
	seq   int
	jobs  map[string]*provisionRun
	order []string
}

func (st *jobStore) add(r *provisionRun) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.seq++
	r.job.Id = fmt.Sprintf("p-%d-%d", time.Now().Unix(), st.seq)
	st.jobs[r.job.Id] = r
	st.order = append(st.order, r.job.Id)
	for len(st.order) > provisionKeepJobs {
		delete(st.jobs, st.order[0])
		st.order = st.order[1:]
	}
}

func (st *jobStore) get(id string) (*provisionRun, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.jobs[id]
	return r, ok
}

func (st *jobStore) all() []*provisionRun {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]*provisionRun, 0, len(st.order))
	for i := len(st.order) - 1; i >= 0; i-- {
		out = append(out, st.jobs[st.order[i]])
	}
	return out
}

func (r *provisionRun) snapshot() ProvisionJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.job
	j.Steps = slices.Clone(r.job.Steps)
	j.Log = slices.Clone(r.job.Log)
	return j
}

// mask hides every secret the job knows about; install.sh echoes the
// generated panel password and API token, and a log must be safe to share.
func (r *provisionRun) mask(s string) string {
	for _, sec := range r.secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "******")
		}
	}
	return s
}

func (r *provisionRun) addSecret(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s != "" {
		r.secrets = append(r.secrets, s)
	}
}

func (r *provisionRun) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := r.mask(fmt.Sprintf(format, args...))
	r.job.Log = append(r.job.Log, line)
	if n := len(r.job.Log) - provisionLogLines; n > 0 {
		r.job.Log = r.job.Log[n:]
	}
}

func (r *provisionRun) step(name, status, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	for i := range r.job.Steps {
		st := &r.job.Steps[i]
		if st.Name != name {
			continue
		}
		if status == "running" {
			st.StartedAt = now
		} else {
			st.FinishedAt = now
		}
		st.Status, st.Detail = status, r.mask(detail)
	}
}

func (r *provisionRun) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.job.Status = "done"
		return
	}
	r.job.Status, r.job.Error = "failed", r.mask(err.Error())
	for i := range r.job.Steps {
		if r.job.Steps[i].Status == "running" {
			r.job.Steps[i].Status, r.job.Steps[i].Detail = "failed", r.job.Error
		}
	}
}

func (s *NodeProvisionService) List() []ProvisionJob {
	runs := provisionJobs.all()
	out := make([]ProvisionJob, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.snapshot())
	}
	return out
}

func (s *NodeProvisionService) Get(id string) (*ProvisionJob, error) {
	r, ok := provisionJobs.get(id)
	if !ok {
		return nil, common.NewErrorf("provisioning job %s not found (jobs do not survive a panel restart)", id)
	}
	j := r.snapshot()
	return &j, nil
}

var nodeLabelRe = func(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// resolveDomain applies the "<name>.<zone>" convenience and finds the zone
// that will carry the node's address record.
func (s *NodeProvisionService) resolveDomain(req *NodeProvisionRequest) (*model.DnsProvider, error) {
	req.Domain = dnsprovider.NormalizeName(req.Domain)
	if req.DnsProviderId > 0 {
		prov, err := s.dns.load(req.DnsProviderId)
		if err != nil {
			return nil, common.NewErrorf("DNS provider %d not found", req.DnsProviderId)
		}
		if req.Domain == "" {
			label := strings.ToLower(req.Name)
			if !nodeLabelRe(label) {
				return nil, common.NewErrorf("name %q is not a DNS label; give the domain explicitly", req.Name)
			}
			req.Domain = label + "." + prov.Zone
		}
		if !dnsprovider.InZone(req.Domain, prov.Zone) {
			return nil, common.NewErrorf("%s is outside zone %s", req.Domain, prov.Zone)
		}
		return prov, nil
	}
	if req.Domain == "" || req.SkipDns {
		return nil, nil
	}
	prov, err := s.dns.ForName(req.Domain)
	if err != nil {
		return nil, common.NewErrorf("no DNS provider manages %s; add one or tick \"DNS is managed elsewhere\"", req.Domain)
	}
	return prov, nil
}

func (s *NodeProvisionService) validate(req *NodeProvisionRequest) (*model.DnsProvider, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	if req.Name == "" || req.Host == "" {
		return nil, common.NewError("name and host are required")
	}
	if strings.TrimSpace(req.SshPassword) == "" && strings.TrimSpace(req.SshPrivateKey) == "" {
		return nil, common.NewError("an SSH password or private key is required")
	}
	if req.SshUser == "" {
		req.SshUser = "root"
	}
	if req.SshPort == 0 {
		req.SshPort = 22
	}
	if req.SshPort < 1 || req.SshPort > 65535 || req.PanelPort < 0 || req.PanelPort > 65535 {
		return nil, common.NewError("ports must be 1-65535")
	}
	switch req.InstallSource {
	case "":
		req.InstallSource = "self"
	case "self", "github":
	default:
		return nil, common.NewErrorf("install source %q is not self or github", req.InstallSource)
	}
	if req.DisablePasswordAuth && strings.TrimSpace(req.SshPrivateKey) == "" {
		return nil, common.NewError("password login can only be disabled when the job itself logs in with a key")
	}
	if k := strings.TrimSpace(req.AuthorizedKey); k != "" && (strings.ContainsAny(k, "\n\r") || !strings.HasPrefix(k, "ssh-") && !strings.HasPrefix(k, "ecdsa-")) {
		return nil, common.NewError("authorized key must be one OpenSSH public key line")
	}
	if exists, err := s.nameTaken(req.Name); err != nil {
		return nil, err
	} else if exists {
		return nil, common.NewErrorf("a node named %q already exists", req.Name)
	}
	return s.resolveDomain(req)
}

func (s *NodeProvisionService) nameTaken(name string) (bool, error) {
	nodes, err := s.nodes.GetAll()
	if err != nil {
		return false, err
	}
	for _, n := range nodes {
		if strings.EqualFold(n.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

// Start validates the request and runs the job in the background.
func (s *NodeProvisionService) Start(req *NodeProvisionRequest) (*ProvisionJob, error) {
	prov, err := s.validate(req)
	if err != nil {
		return nil, err
	}
	run := &provisionRun{job: ProvisionJob{
		Name: req.Name, Host: req.Host, Domain: req.Domain, Status: "running", CreatedAt: time.Now().Unix(),
	}}
	for _, n := range []string{stepPreflight, stepDNS, stepUpload, stepInstall, stepRegister, stepVerify, stepHarden} {
		run.job.Steps = append(run.job.Steps, ProvisionStep{Name: n, Status: "pending"})
	}
	run.addSecret(req.SshPassword)
	provisionJobs.add(run)

	reqCopy := *req
	common.GoRecover("node-provision", func() {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		err := s.run(ctx, run, &reqCopy, prov)
		if err != nil {
			logger.Warning("provision", run.job.Id, "failed:", run.mask(err.Error()))
		}
		run.finish(err)
	})
	j := run.snapshot()
	return &j, nil
}

func randomString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

func randomPanelPort() int {
	v, _ := rand.Int(rand.Reader, big.NewInt(40000))
	return 20000 + int(v.Int64())
}

// goArchFor maps `uname -m` to the arch names release archives use.
func goArchFor(uname string) string {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i686":
		return "386"
	case "s390x":
		return "s390x"
	}
	return ""
}

func (s *NodeProvisionService) run(ctx context.Context, run *provisionRun, req *NodeProvisionRequest, prov *model.DnsProvider) error {
	// preflight: reach the box, make sure it is ours to overwrite.
	run.step(stepPreflight, "running", "")
	run.logf("== connecting to %s@%s:%d", req.SshUser, req.Host, req.SshPort)
	sess, err := provisionDialer(ctx, provision.Target{
		Host: req.Host, Port: req.SshPort, User: req.SshUser,
		Password: req.SshPassword, PrivateKey: req.SshPrivateKey, HostKeySHA256: req.HostKeySha256,
	})
	if err != nil {
		return err
	}
	defer sess.Close()
	run.mu.Lock()
	run.job.HostKey = sess.HostKey
	run.mu.Unlock()
	run.logf("host key %s", sess.HostKey)

	facts, err := sess.Output(ctx, `id -u; uname -m; . /etc/os-release 2>/dev/null; echo "${ID:-unknown} ${VERSION_ID:-}"; `+
		`if [ -e /usr/local/x-ui/x-ui ] || [ -e /etc/x-ui/x-ui.db ]; then echo installed; else echo fresh; fi`)
	if err != nil {
		return err
	}
	lines := strings.Split(facts, "\n")
	if len(lines) < 4 {
		return fmt.Errorf("unexpected preflight output: %q", facts)
	}
	if strings.TrimSpace(lines[0]) != "0" {
		return errors.New("the SSH user must be root (install.sh writes /usr/local and systemd units)")
	}
	arch := goArchFor(lines[1])
	if arch == "" {
		return fmt.Errorf("unsupported architecture %q", lines[1])
	}
	if strings.TrimSpace(lines[3]) == "installed" && !req.Force {
		return errors.New("x-ui is already installed there; refusing to overwrite a live panel (tick Force to reinstall)")
	}
	run.step(stepPreflight, "done", fmt.Sprintf("%s, %s", strings.TrimSpace(lines[2]), arch))

	// dns: point <domain> at the host before install.sh asks the CA for it.
	if err := s.stepDNS(ctx, run, req, prov); err != nil {
		return err
	}

	// upload: installer, and this panel's own build when the arch matches.
	run.step(stepUpload, "running", "")
	if len(releaseAssets.InstallScript) == 0 {
		return errors.New("this build does not embed install.sh")
	}
	if _, err := sess.Output(ctx, "mkdir -p -m 700 "+remoteWorkDir); err != nil {
		return err
	}
	if err := sess.Upload(ctx, remoteWorkDir+"/install.sh", 0o700, strings.NewReader(string(releaseAssets.InstallScript))); err != nil {
		return err
	}
	archive := ""
	uploadDetail := "install.sh; release downloaded from GitHub by the node"
	if req.InstallSource == "self" {
		if arch != goruntime.GOARCH {
			run.logf("node is %s, this panel is %s: falling back to the GitHub release", arch, goruntime.GOARCH)
		} else {
			archive = remoteWorkDir + "/x-ui-linux-" + arch + ".tar.gz"
			if err := s.uploadSelf(ctx, run, sess, archive); err != nil {
				return err
			}
			uploadDetail = "install.sh and this panel's own build"
		}
	}
	run.step(stepUpload, "done", uploadDetail)

	// install: run install.sh unattended with generated credentials.
	run.step(stepInstall, "running", "")
	panelUser, panelPass, basePath := randomString(10), randomString(20), randomString(12)
	run.addSecret(panelPass)
	port := req.PanelPort
	if port == 0 {
		port = randomPanelPort()
	}
	sslMode := "ip"
	if req.Domain != "" {
		sslMode = "domain"
	}
	env := map[string]string{
		"XUI_NONINTERACTIVE": "1",
		"XUI_USERNAME":       panelUser,
		"XUI_PASSWORD":       panelPass,
		"XUI_PANEL_PORT":     strconv.Itoa(port),
		"XUI_WEB_BASE_PATH":  basePath,
		"XUI_SSL_MODE":       sslMode,
		"XUI_DOMAIN":         req.Domain,
		"XUI_ACME_EMAIL":     req.AcmeEmail,
		"XUI_SERVER_IP":      req.Host,
		"XUI_ARCHIVE":        archive,
		"DEBIAN_FRONTEND":    "noninteractive",
	}
	var envFile strings.Builder
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if env[k] != "" {
			fmt.Fprintf(&envFile, "%s=%s\n", k, provision.ShellQuote(env[k]))
		}
	}
	if err := sess.Upload(ctx, remoteWorkDir+"/env", 0o600, strings.NewReader(envFile.String())); err != nil {
		return err
	}
	run.logf("== running install.sh (ssl=%s, port=%d)", sslMode, port)
	code, err := sess.Run(ctx, "cd "+remoteWorkDir+" && set -a && . ./env && set +a && bash ./install.sh </dev/null 2>&1",
		nil, func(l string) { run.logf("%s", l) })
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("install.sh exited %d; see the log", code)
	}
	result, err := sess.Output(ctx, "cat /etc/x-ui/install-result.env")
	if err != nil {
		return fmt.Errorf("install finished but left no install-result.env: %w", err)
	}
	vals := parseEnvFile(result)
	run.addSecret(vals["XUI_API_TOKEN"])
	if vals["XUI_API_TOKEN"] == "" {
		return errors.New("install-result.env has no API token")
	}
	run.step(stepInstall, "done", vals["XUI_ACCESS_URL"])
	run.mu.Lock()
	run.job.PanelUrl = vals["XUI_ACCESS_URL"]
	run.mu.Unlock()

	// register + verify: add the node exactly as the Nodes form would.
	nodeID, err := s.register(ctx, run, req, vals)
	if err != nil {
		return err
	}
	run.mu.Lock()
	run.job.NodeId = nodeID
	run.mu.Unlock()

	// harden: optional, and only after the node is reachable through the API.
	if err := s.harden(ctx, run, sess, req); err != nil {
		return err
	}
	_, _ = sess.Output(ctx, "rm -rf "+remoteWorkDir)
	run.logf("== done: node %q is registered", req.Name)
	return nil
}

func (s *NodeProvisionService) stepDNS(ctx context.Context, run *provisionRun, req *NodeProvisionRequest, prov *model.DnsProvider) error {
	if prov == nil {
		detail := "no domain: the node is installed on its IP"
		if req.Domain != "" {
			detail = "managed elsewhere: " + req.Domain + " must already point at " + req.Host
		}
		run.step(stepDNS, "skipped", detail)
		return nil
	}
	run.step(stepDNS, "running", "")
	addr, err := netip.ParseAddr(req.Host)
	if err != nil {
		ips, lerr := net.DefaultResolver.LookupHost(ctx, req.Host)
		if lerr != nil || len(ips) == 0 {
			return fmt.Errorf("host %q is neither an IP nor resolvable", req.Host)
		}
		addr, _ = netip.ParseAddr(ips[0])
	}
	rtype := "A"
	if addr.Unmap().Is6() {
		rtype = "AAAA"
	}
	client, zone, err := s.dns.ClientFor(prov.Id)
	if err != nil {
		return err
	}
	if err := client.SetRecords(ctx, req.Domain, rtype, []string{addr.Unmap().String()}, 300); err != nil {
		return err
	}
	run.logf("%s %s -> %s written to %s", req.Domain, rtype, addr.Unmap(), zone)
	if err := dnsprovider.WaitVisible(ctx, zone, req.Domain, rtype, []string{addr.Unmap().String()}, 3*time.Minute); err != nil {
		run.logf("warning: %v; continuing, the CA may still see it", err)
	}
	run.step(stepDNS, "done", fmt.Sprintf("%s %s %s", req.Domain, rtype, addr.Unmap()))
	return nil
}

func (s *NodeProvisionService) uploadSelf(ctx context.Context, run *provisionRun, sess *provision.Session, dst string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	binDir, err := filepath.Abs(config.GetBinFolderPath())
	if err != nil {
		return err
	}
	run.logf("packing %s with %s", exe, binDir)
	pr, pw := io.Pipe()
	go func() {
		bw := bufio.NewWriterSize(pw, 1<<20)
		err := provision.WriteReleaseArchive(bw, provision.ReleaseSource{Binary: exe, BinDir: binDir, Fallback: releaseAssets.Files})
		if err == nil {
			err = bw.Flush()
		}
		_ = pw.CloseWithError(err)
	}()
	counter := &countingReader{r: pr}
	start := time.Now()
	if err := sess.Upload(ctx, dst, 0o600, counter); err != nil {
		return err
	}
	run.logf("uploaded %.1f MiB in %s", float64(counter.n)/(1<<20), time.Since(start).Round(time.Second))
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// parseEnvFile reads install-result.env, which install.sh writes with
// printf %q: plain words, or $'...' / '...' quoted values.
func parseEnvFile(s string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || k == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, "$'") && strings.HasSuffix(v, "'"):
			v = strings.ReplaceAll(v[2:len(v)-1], `\'`, `'`)
		case strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2:
			v = v[1 : len(v)-1]
		default:
			v = strings.ReplaceAll(v, `\`, "")
		}
		out[k] = v
	}
	return out
}

func (s *NodeProvisionService) register(ctx context.Context, run *provisionRun, req *NodeProvisionRequest, vals map[string]string) (int, error) {
	run.step(stepRegister, "running", "")
	scheme := "http"
	if strings.HasPrefix(vals["XUI_ACCESS_URL"], "https://") {
		scheme = "https"
	}
	port, _ := strconv.Atoi(vals["XUI_PANEL_PORT"])
	address := req.Host
	if req.Domain != "" && scheme == "https" {
		address = req.Domain
	}
	token := vals["XUI_API_TOKEN"]
	mreq := &NodeMutationRequest{
		Name:          req.Name,
		Remark:        "provisioned " + time.Now().Format("2006-01-02"),
		Scheme:        scheme,
		Address:       address,
		Port:          port,
		BasePath:      "/" + strings.Trim(vals["XUI_WEB_BASE_PATH"], "/") + "/",
		ApiToken:      &token,
		Enable:        true,
		TlsVerifyMode: "verify",

		AllowPrivateAddress: req.AllowPrivateAddress,
	}
	probeNode := mreq.toNode()
	if scheme == "https" {
		if _, err := s.nodes.Probe(ctx, probeNode); err != nil {
			fp, ferr := s.nodes.FetchCertFingerprint(ctx, probeNode)
			if ferr != nil {
				return 0, fmt.Errorf("node answers neither with a trusted certificate (%v) nor at all (%v)", err, ferr)
			}
			run.logf("certificate not publicly trusted (%v); pinning %s", err, fp)
			mreq.TlsVerifyMode, mreq.PinnedCertSha256 = "pin", fp
		}
	} else {
		run.logf("warning: the node serves plain HTTP; its API token crosses the network unencrypted")
	}
	view, err := s.nodes.CreateFromRequest(mreq)
	if err != nil {
		return 0, err
	}
	run.step(stepRegister, "done", fmt.Sprintf("%s://%s:%d%s (%s)", scheme, address, port, mreq.BasePath, mreq.TlsVerifyMode))

	run.step(stepVerify, "running", "")
	n, err := s.nodes.GetById(view.Id)
	if err != nil {
		return view.Id, err
	}
	var patch HeartbeatPatch
	for attempt := 1; ; attempt++ {
		if patch, err = s.nodes.Probe(ctx, n); err == nil {
			break
		}
		if attempt == 6 {
			return view.Id, fmt.Errorf("node registered but its API does not answer: %w", err)
		}
		select {
		case <-ctx.Done():
			return view.Id, ctx.Err()
		case <-time.After(provisionRetryDelay):
		}
	}
	_ = s.nodes.UpdateHeartbeat(n.Id, patch)
	run.step(stepVerify, "done", fmt.Sprintf("panel %s, xray %s, %d ms", patch.PanelVersion, patch.XrayVersion, patch.LatencyMs))
	return view.Id, nil
}

func (s *NodeProvisionService) harden(ctx context.Context, run *provisionRun, sess *provision.Session, req *NodeProvisionRequest) error {
	key := strings.TrimSpace(req.AuthorizedKey)
	if key == "" && !req.DisablePasswordAuth {
		run.step(stepHarden, "skipped", "nothing requested")
		return nil
	}
	run.step(stepHarden, "running", "")
	var done []string
	if key != "" {
		cmd := "install -d -m 700 /root/.ssh && touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && " +
			"(grep -qxF " + provision.ShellQuote(key) + " /root/.ssh/authorized_keys || echo " + provision.ShellQuote(key) + " >> /root/.ssh/authorized_keys)"
		if _, err := sess.Output(ctx, cmd); err != nil {
			return err
		}
		done = append(done, "authorized key added")
	}
	if req.DisablePasswordAuth {
		if !sess.UsedKey {
			return errors.New("refusing to disable password login: this session did not authenticate with a key")
		}
		cmd := "mkdir -p /etc/ssh/sshd_config.d && printf 'PasswordAuthentication no\\nKbdInteractiveAuthentication no\\n' > /etc/ssh/sshd_config.d/00-fbui.conf && " +
			"sshd -t && (systemctl reload ssh 2>/dev/null || systemctl reload sshd)"
		if _, err := sess.Output(ctx, cmd); err != nil {
			_, _ = sess.Output(ctx, "rm -f /etc/ssh/sshd_config.d/00-fbui.conf")
			return fmt.Errorf("could not disable password login (reverted): %w", err)
		}
		done = append(done, "password login disabled")
	}
	run.step(stepHarden, "done", strings.Join(done, ", "))
	return nil
}
