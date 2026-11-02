package provision

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
)

// Defaults for where the agent lives on a server.
const (
	DefaultDir     = "/etc/fbnode"
	DefaultBin     = "/usr/local/bin/fbnode"
	DefaultUnit    = "/etc/systemd/system/fbnode.service"
	DefaultXrayBin = "/usr/local/x-ui/bin/xray-linux-amd64"
	// one argument must stay below Linux' 128 KiB MAX_ARG_STRLEN, and the
	// command has the path too; 96 KiB of base64 is 72 KiB of file.
	uploadChunkSize = 96 * 1024
)

// InstallRequest is everything the install needs; the identity is redeemed
// by the caller, so the key material is created once and uploaded.
type InstallRequest struct {
	Target   Target
	Auth     Auth
	HostKey  string
	Node     string
	Masters  map[string]string // master name -> tls://host:port
	Identity enroll.Result
	Binary   []byte
	Dir      string
	BinPath  string
	UnitPath string
	XrayBin  string
	Wait     time.Duration // how long to wait for the unit to become active
}

// InstallResult is what happened on the server.
type InstallResult struct {
	Node          string `json:"node"`
	Binary        string `json:"binary"`
	Unit          string `json:"unit"`
	SHA256        string `json:"sha256"`
	ServiceActive bool   `json:"serviceActive"`
}

// cmdRunner runs a command and uploads a file; *sshRunner is one.
type cmdRunner interface {
	Run(ctx context.Context, cmd string) (string, error)
	Upload(ctx context.Context, path string, data []byte, mode string) error
}

// Install puts the agent on a server: uploads the binary, the identity and a
// systemd unit, then starts it and waits for it to stay up. Any failure
// undoes what was done.
func Install(ctx context.Context, req InstallRequest) (InstallResult, error) {
	if err := req.normalize(); err != nil {
		return InstallResult{}, err
	}
	client, err := dial(ctx, req.Target, req.Auth, req.HostKey)
	if err != nil {
		return InstallResult{}, err
	}
	defer client.Close()
	return install(ctx, sshRunner{client: client}, req)
}

func (r *InstallRequest) normalize() error {
	if r.Node == "" {
		return errors.New("provision: a node name is required")
	}
	if len(r.Binary) == 0 {
		return errors.New("provision: the agent binary is empty")
	}
	if r.Identity.Cert == "" || r.Identity.Key == "" || r.Identity.CA == "" {
		return errors.New("provision: the node's identity is missing")
	}
	if r.Identity.Node != "" && r.Identity.Node != r.Node {
		return fmt.Errorf("provision: identity is for %q, not %q", r.Identity.Node, r.Node)
	}
	if len(r.Masters) == 0 {
		return errors.New("provision: at least one master is required")
	}
	if r.Dir == "" {
		r.Dir = DefaultDir
	}
	if r.BinPath == "" {
		r.BinPath = DefaultBin
	}
	if r.UnitPath == "" {
		r.UnitPath = DefaultUnit
	}
	if r.XrayBin == "" {
		r.XrayBin = DefaultXrayBin
	}
	if r.Wait <= 0 {
		r.Wait = 30 * time.Second
	}
	return nil
}

// install is the sequence, with an undo list built as it goes.
func install(ctx context.Context, r cmdRunner, req InstallRequest) (InstallResult, error) {
	var undo []string
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			_, _ = r.Run(ctx, undo[i])
		}
	}
	fail := func(format string, args ...any) (InstallResult, error) {
		rollback()
		return InstallResult{}, fmt.Errorf(format, args...)
	}

	if out, err := r.Run(ctx, "test -x "+sh(req.XrayBin)+" && echo yes"); err != nil || strings.TrimSpace(out) != "yes" {
		return InstallResult{}, fmt.Errorf("provision: %s is not executable on the server; install the core first", req.XrayBin)
	}
	if out, err := r.Run(ctx, "id -u"); err != nil || strings.TrimSpace(out) != "0" {
		return InstallResult{}, errors.New("provision: the install needs root on the server")
	}

	if _, err := r.Run(ctx, "mkdir -p "+sh(req.Dir)+" "+sh(dirOf(req.BinPath))+" "+sh(dirOf(req.UnitPath))); err != nil {
		return InstallResult{}, fmt.Errorf("provision: create directories: %w", err)
	}
	undo = append(undo, "rm -f "+sh(req.BinPath)+" "+sh(req.UnitPath)+
		" "+sh(req.Dir+"/ca.pem")+" "+sh(req.Dir+"/cert.pem")+" "+sh(req.Dir+"/key.pem")+" "+sh(req.Dir+"/config.json"))
	undo = append(undo, "systemctl disable --now fbnode || true", "systemctl daemon-reload || true")

	if err := r.Upload(ctx, req.BinPath, req.Binary, "0755"); err != nil {
		return fail("provision: upload the agent: %w", err)
	}
	sum := sha256.Sum256(req.Binary)
	want := hex.EncodeToString(sum[:])
	if out, err := r.Run(ctx, "sha256sum "+sh(req.BinPath)+" | cut -d' ' -f1"); err != nil || strings.TrimSpace(out) != want {
		return fail("provision: the uploaded agent does not match (got %q, want %s)", strings.TrimSpace(out), want)
	}
	if out, err := r.Run(ctx, sh(req.BinPath)+" enroll 2>&1 | head -n1"); err == nil && !strings.Contains(out, "enroll") {
		// a binary that cannot parse its own CLI is not the agent at all
		return fail("provision: the uploaded binary does not look like fbnode: %s", strings.TrimSpace(out))
	}

	for name, body := range map[string]string{"ca.pem": req.Identity.CA, "cert.pem": req.Identity.Cert, "key.pem": req.Identity.Key} {
		if err := r.Upload(ctx, req.Dir+"/"+name, []byte(body), "0600"); err != nil {
			return fail("provision: upload %s: %w", name, err)
		}
	}
	cfg := map[string]any{
		"node": req.Node, "masters": req.Masters,
		"ca":      req.Dir + "/ca.pem",
		"cert":    req.Dir + "/cert.pem",
		"key":     req.Dir + "/key.pem",
		"dataDir": req.Dir + "/data",
		"xray":    map[string]string{"bin": req.XrayBin},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fail("provision: config: %w", err)
	}
	if err := r.Upload(ctx, req.Dir+"/config.json", raw, "0600"); err != nil {
		return fail("provision: upload the config: %w", err)
	}
	if err := r.Upload(ctx, req.UnitPath, []byte(unit(req.BinPath, req.Dir+"/config.json")), "0644"); err != nil {
		return fail("provision: upload the unit: %w", err)
	}
	if out, err := r.Run(ctx, "systemctl daemon-reload"); err != nil {
		return fail("provision: daemon-reload: %v %s", err, out)
	}
	if out, err := r.Run(ctx, "systemctl enable fbnode"); err != nil {
		return fail("provision: enable fbnode: %v %s", err, out)
	}
	if out, err := r.Run(ctx, "systemctl start fbnode"); err != nil {
		return fail("provision: start fbnode: %v %s", err, out)
	}

	deadline := time.Now().Add(req.Wait)
	for {
		out, _ := r.Run(ctx, "systemctl is-active fbnode")
		if strings.TrimSpace(out) == "active" {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := r.Run(ctx, "journalctl -u fbnode -n 20 --no-pager 2>&1 | tail -n 20")
			return fail("provision: fbnode did not become active in %s: %s", req.Wait, strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			return fail("provision: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return InstallResult{Node: req.Node, Binary: req.BinPath, Unit: req.UnitPath, SHA256: want, ServiceActive: true}, nil
}

func unit(bin, config string) string {
	return `[Unit]
Description=fbui node agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=` + bin + ` -c ` + config + `
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

// sh quotes one argument for a POSIX shell.
func sh(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// dial opens an SSH client to the target.
func dial(ctx context.Context, t Target, a Auth, pinned string) (*ssh.Client, error) {
	if t.Host == "" || t.User == "" {
		return nil, errors.New("provision: host and user are required")
	}
	if t.Port <= 0 {
		t.Port = 22
	}
	cfg, err := clientConfig(t.User, a, pinned)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		return nil, fmt.Errorf("provision: dial %s: %w", t.Host, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("provision: ssh %s: %w", t.Host, err)
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// Upload writes data through the shell in base64 chunks, so no sftp
// subsystem is needed on the target.
func (r sshRunner) Upload(ctx context.Context, path string, data []byte, mode string) error {
	if out, err := r.Run(ctx, ": > "+sh(path)); err != nil {
		return fmt.Errorf("create %s: %v %s", path, err, strings.TrimSpace(out))
	}
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 0 {
		n := min(uploadChunkSize, len(enc))
		chunk := enc[:n]
		enc = enc[n:]
		if out, err := r.Run(ctx, "printf %s "+sh(chunk)+" | base64 -d >> "+sh(path)); err != nil {
			return fmt.Errorf("write %s: %v %s", path, err, strings.TrimSpace(out))
		}
	}
	if out, err := r.Run(ctx, "chmod "+mode+" "+sh(path)); err != nil {
		return fmt.Errorf("chmod %s: %v %s", path, err, strings.TrimSpace(out))
	}
	return nil
}
