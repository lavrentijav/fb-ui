package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
)

// testMachine is an SSH server whose commands run in a sandbox: a fake
// systemctl and id on PATH record and fake the privileged parts, so the
// whole install sequence runs without touching the test host.
type testMachine struct {
	host, port string
	root       string
	state      string
}

func startMachine(t *testing.T, startActive bool) *testMachine {
	t.Helper()
	root := t.TempDir()
	fake := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "service.state")
	active := "failed"
	if startActive {
		active = "active"
	}
	writeScript(t, filepath.Join(fake, "systemctl"), `#!/bin/sh
state="`+state+`"
case "$1" in
daemon-reload) exit 0 ;;
enable) echo enabled > "$state"; exit 0 ;;
start) echo `+active+` > "$state"; exit 0 ;;
restart) echo `+active+` > "$state"; exit 0 ;;
is-active) if [ "$(cat "$state" 2>/dev/null)" = active ]; then echo active; exit 0; fi; echo inactive; exit 1 ;;
disable|stop) echo stopped > "$state"; exit 0 ;;
esac
exit 0
`)
	writeScript(t, filepath.Join(fake, "id"), "#!/bin/sh\necho 0\n")

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveMachine(c, cfg, fake)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	return &testMachine{host: h, port: p, root: root, state: state}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func serveMachine(c net.Conn, cfg *ssh.ServerConfig, path string) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				_ = req.Reply(true, nil)
				cmd := exec.Command("sh", "-c", payload.Command)
				cmd.Env = append(os.Environ(), "PATH="+path+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				status := uint32(0)
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					status = uint32(exitErr.ExitCode())
				} else if err != nil {
					status = 1
				}
				_, _ = channel.Write(out)
				_ = channel.CloseWrite()
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

// fakeAgent is a stand-in for the fbnode binary: it answers the enroll
// subcommand the installer probes with.
func fakeAgent() []byte {
	return []byte("#!/bin/sh\necho 'fbnode enroll: -master, -token, -node and -masters are required' >&2\nexit 1\n")
}

func installRequest(m *testMachine) InstallRequest {
	p := func(n string) string { return filepath.Join(m.root, n) }
	return InstallRequest{
		Target:   Target{Host: m.host, User: "root"},
		Auth:     Auth{Kind: "password", Password: "x"},
		Node:     "ru4",
		Masters:  map[string]string{"fi1": "tls://fi1.example:443"},
		Identity: enroll.Result{Node: "ru4", CA: "CA-PEM\n", Cert: "CERT-PEM\n", Key: "KEY-PEM\n"},
		Binary:   fakeAgent(),
		Dir:      p("etc/fbnode"),
		BinPath:  p("usr/local/bin/fbnode"),
		UnitPath: p("etc/systemd/system/fbnode.service"),
		XrayBin:  p("usr/local/x-ui/xray"),
		Wait:     3 * time.Second,
	}
}

func TestInstallUploadsAndStarts(t *testing.T) {
	m := startMachine(t, true)
	writeScript(t, filepath.Join(m.root, "usr/local/x-ui/xray"), "#!/bin/sh\n")
	req := installRequest(m)
	req.Target.Port = atoi(m.port)

	res, err := Install(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ServiceActive || res.Node != "ru4" || res.SHA256 == "" {
		t.Fatalf("result %+v", res)
	}
	for path, want := range map[string]string{
		req.BinPath:              "fbnode enroll",
		req.Dir + "/ca.pem":      "CA-PEM",
		req.Dir + "/key.pem":     "KEY-PEM",
		req.Dir + "/config.json": `"node": "ru4"`,
		req.UnitPath:             "ExecStart=" + req.BinPath,
	} {
		b, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), want) {
			t.Fatalf("%s: %v %q", path, err, b)
		}
	}
	if mode := modeOf(t, req.Dir+"/key.pem"); mode != "0600" {
		t.Fatalf("key.pem mode %s", mode)
	}
	if b, _ := os.ReadFile(m.state); strings.TrimSpace(string(b)) != "active" {
		t.Fatalf("service state %q", b)
	}
}

func TestInstallRollsBackWhenItDoesNotStart(t *testing.T) {
	m := startMachine(t, false)
	writeScript(t, filepath.Join(m.root, "usr/local/x-ui/xray"), "#!/bin/sh\n")
	req := installRequest(m)
	req.Target.Port = atoi(m.port)
	req.Wait = 100 * time.Millisecond
	if _, err := Install(context.Background(), req); err == nil || !strings.Contains(err.Error(), "did not become active") {
		t.Fatalf("err %v", err)
	}
	for _, path := range []string{req.BinPath, req.UnitPath, req.Dir + "/config.json", req.Dir + "/key.pem"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived the rollback", path)
		}
	}
}

func TestInstallRefusesBeforeChangingAnything(t *testing.T) {
	m := startMachine(t, true)
	req := installRequest(m)
	req.Target.Port = atoi(m.port)
	if _, err := Install(context.Background(), req); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("no core: %v", err)
	}
	writeScript(t, filepath.Join(m.root, "usr/local/x-ui/xray"), "#!/bin/sh\n")
	req.Binary = nil
	if _, err := Install(context.Background(), req); err == nil || !strings.Contains(err.Error(), "binary is empty") {
		t.Fatalf("no binary: %v", err)
	}
	req.Binary = fakeAgent()
	req.Identity = enroll.Result{Node: "de2", CA: "c", Cert: "c", Key: "k"}
	if _, err := Install(context.Background(), req); err == nil || !strings.Contains(err.Error(), "identity is for") {
		t.Fatalf("foreign identity: %v", err)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func modeOf(t *testing.T, path string) string {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return "0" + strconv.FormatUint(uint64(st.Mode().Perm()), 8)
}
