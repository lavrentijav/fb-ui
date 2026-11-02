// Package provision inspects a server over SSH before anything is installed:
// operating system, architecture, listeners, free disk and whether a panel is
// already there. It changes nothing on the target.
package provision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Auth is how to log in: a password or a private key (PEM, optional
// passphrase). It is used for one call and never stored.
type Auth struct {
	Kind       string `json:"kind"` // password | key
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// Target is the server to inspect.
type Target struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

// Result is what preflight found; it changes nothing.
type Result struct {
	HostKey     string   `json:"hostKey"` // SHA256:... — pin this for the next step
	OS          string   `json:"os"`
	OSID        string   `json:"osId"`
	Arch        string   `json:"arch"`
	Root        bool     `json:"root"`
	Systemd     bool     `json:"systemd"`
	Listeners   []int    `json:"listeners"`
	PortsFree   Ports    `json:"portsFree"`
	DiskFree    int64    `json:"diskFreeBytes"`
	PanelOnHost bool     `json:"panelOnHost"` // x-ui binary present
	AgentOnHost bool     `json:"agentOnHost"` // fbnode binary present
	Warnings    []string `json:"warnings,omitempty"`
}

// Ports are the ports the install needs.
type Ports struct {
	HTTP  bool `json:"80"`
	HTTPS bool `json:"443"`
	Alt   bool `json:"2053"`
}

// Preflight connects, reads the facts and disconnects. pinned is the host
// key fingerprint to require; empty accepts and returns whatever it saw, so
// an operator can pin it before the first change.
func Preflight(ctx context.Context, t Target, a Auth, pinned string) (Result, error) {
	if t.Host == "" || t.User == "" {
		return Result{}, errors.New("provision: host and user are required")
	}
	if t.Port <= 0 {
		t.Port = 22
	}
	cfg, err := clientConfig(t.User, a, pinned)
	if err != nil {
		return Result{}, err
	}
	var seen string
	inner := cfg.HostKeyCallback
	cfg.HostKeyCallback = func(host string, remote net.Addr, key ssh.PublicKey) error {
		seen = ssh.FingerprintSHA256(key)
		return inner(host, remote, key)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		return Result{}, fmt.Errorf("provision: dial %s: %w", t.Host, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), cfg)
	if err != nil {
		conn.Close()
		return Result{}, fmt.Errorf("provision: ssh %s: %w", t.Host, err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	res, err := inspect(ctx, sshRunner{client: client})
	if err != nil {
		return Result{}, err
	}
	res.HostKey = seen
	return res, nil
}

func clientConfig(user string, a Auth, pinned string) (*ssh.ClientConfig, error) {
	var methods []ssh.AuthMethod
	switch a.Kind {
	case "password":
		if a.Password == "" {
			return nil, errors.New("provision: a password is required")
		}
		methods = append(methods, ssh.Password(a.Password))
	case "key":
		var (
			signer ssh.Signer
			err    error
		)
		if a.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(a.Key), []byte(a.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(a.Key))
		}
		if err != nil {
			return nil, fmt.Errorf("provision: private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	default:
		return nil, errors.New("provision: auth kind must be password or key")
	}
	hostKey := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if pinned == "" {
			return nil
		}
		if got := ssh.FingerprintSHA256(key); got != pinned {
			return fmt.Errorf("provision: host key is %s, pinned %s", got, pinned)
		}
		return nil
	}
	return &ssh.ClientConfig{
		User: user, Auth: methods, HostKeyCallback: hostKey,
		Timeout: 10 * time.Second,
	}, nil
}

// runner runs one command on the target.
type runner interface {
	Run(ctx context.Context, cmd string) (string, error)
}

type sshRunner struct{ client *ssh.Client }

func (r sshRunner) Run(ctx context.Context, cmd string) (string, error) {
	s, err := r.client.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	type out struct {
		b   []byte
		err error
	}
	done := make(chan out, 1)
	go func() {
		b, err := s.Output(cmd)
		done <- out{b, err}
	}()
	select {
	case <-ctx.Done():
		_ = s.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case o := <-done:
		return string(o.b), o.err
	}
}

const probeScript = `
echo "@@os"
cat /etc/os-release 2>/dev/null
echo "@@uname"
uname -s; uname -m
echo "@@uid"
id -u
echo "@@systemd"
command -v systemctl 2>/dev/null
echo "@@bins"
command -v x-ui 2>/dev/null; command -v fbnode 2>/dev/null
echo "@@listen"
{ ss -ltnH 2>/dev/null || netstat -ltn 2>/dev/null; } || true
echo "@@disk"
df -Pk / 2>/dev/null | tail -n 1
echo "@@end"
`

func inspect(ctx context.Context, r runner) (Result, error) {
	raw, err := r.Run(ctx, "sh -c '"+probeScript+"'")
	if err != nil {
		return Result{}, fmt.Errorf("provision: probe: %w", err)
	}
	return parse(raw), nil
}

// parse turns the marked sections into a Result.
func parse(raw string) Result {
	sections := map[string]string{}
	cur := ""
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "@@") {
			if cur != "" {
				sections[cur] = b.String()
			}
			b.Reset()
			cur = strings.TrimPrefix(line, "@@")
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	var res Result
	for _, line := range strings.Split(sections["os"], "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "PRETTY_NAME":
			res.OS = v
		case "ID":
			res.OSID = v
		}
	}
	uname := strings.Fields(sections["uname"])
	if len(uname) >= 2 {
		res.Arch = arch(uname[1])
	}
	res.Root = strings.TrimSpace(sections["uid"]) == "0"
	res.Systemd = strings.TrimSpace(sections["systemd"]) != ""
	bins := strings.Fields(sections["bins"])
	for _, p := range bins {
		switch {
		case strings.HasSuffix(p, "/x-ui"):
			res.PanelOnHost = true
		case strings.HasSuffix(p, "/fbnode"):
			res.AgentOnHost = true
		}
	}
	used := map[int]bool{}
	for _, line := range strings.Split(sections["listen"], "\n") {
		if p, ok := listenPort(line); ok {
			used[p] = true
		}
	}
	for p := range used {
		res.Listeners = append(res.Listeners, p)
	}
	sortInts(res.Listeners)
	res.PortsFree = Ports{HTTP: !used[80], HTTPS: !used[443], Alt: !used[2053]}
	if f := strings.Fields(sections["disk"]); len(f) >= 4 {
		if kb, err := strconv.ParseInt(f[3], 10, 64); err == nil {
			res.DiskFree = kb * 1024
		}
	}
	switch {
	case !res.Root:
		res.Warnings = append(res.Warnings, "the user is not root; installing needs root or sudo")
	case res.OSID == "":
		res.Warnings = append(res.Warnings, "could not read /etc/os-release")
	case !res.PortsFree.HTTPS:
		res.Warnings = append(res.Warnings, "port 443 is already in use")
	case res.PanelOnHost:
		res.Warnings = append(res.Warnings, "a panel (x-ui) binary is already on this server")
	case res.AgentOnHost:
		res.Warnings = append(res.Warnings, "an fbnode binary is already on this server")
	}
	if res.DiskFree > 0 && res.DiskFree < 512<<20 {
		res.Warnings = append(res.Warnings, "less than 512 MB free on /")
	}
	return res
}

// arch maps uname -m to the Go name used by release binaries.
func arch(m string) string {
	switch strings.ToLower(m) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv7", "armhf":
		return "arm"
	case "i386", "i686":
		return "386"
	}
	return strings.ToLower(m)
}

// listenPort reads a port from an `ss -ltnH` or `netstat -ltn` line.
func listenPort(line string) (int, bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return 0, false
	}
	// ss: State Recv-Q Send-Q Local:Port Peer:Port
	// netstat: tcp 0 0 Local:Port Peer:Port
	addr := f[3]
	if !strings.Contains(addr, ":") && len(f) >= 5 {
		addr = f[3] // keep as is
	}
	i := strings.LastIndex(addr, ":")
	if i < 0 || i == len(addr)-1 {
		return 0, false
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
