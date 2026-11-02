//go:build netlab

package netlab

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// testSSHD is an sshd in the lab's own netns: a master installs "onto" it
// through the real SSH path, and a fake systemctl starts the unit it wrote.
func startTestSSHD(t *testing.T, root string) (host string, port int) {
	t.Helper()
	fake := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(root, "etc/systemd/system/fbnode.service")
	state := filepath.Join(root, "service.state")
	pidfile := filepath.Join(root, "service.pid")
	log := filepath.Join(root, "service.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("id", "#!/bin/sh\necho 0\n")
	write("systemctl", `#!/bin/sh
unit="`+unit+`"
case "$1" in
daemon-reload) exit 0 ;;
enable) exit 0 ;;
start) nohup sh -c "$(grep '^ExecStart=' "$unit" | cut -d= -f2-)" >> "`+log+`" 2>&1 & echo $! > "`+pidfile+`"; echo active > "`+state+`"; exit 0 ;;
is-active) if kill -0 "$(cat "`+pidfile+`" 2>/dev/null)" 2>/dev/null; then echo active; exit 0; fi; echo inactive; exit 1 ;;
stop|disable) kill "$(cat "`+pidfile+`" 2>/dev/null)" 2>/dev/null; echo stopped > "`+state+`"; exit 0 ;;
esac
exit 0
`)

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == "root" && string(p) == "labpass" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)
	// the bridge address is reachable from every lab namespace
	ln, err := net.Listen("tcp", "10.0.0.1:0")
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
			go serveTestSSH(c, cfg, fake, log)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return h, n
}

func serveTestSSH(c net.Conn, cfg *ssh.ServerConfig, path, log string) {
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
				var status uint32
				if ee, ok := err.(*exec.ExitError); ok {
					status = uint32(ee.ExitCode())
				} else if err != nil {
					status = 1
				}
				_, _ = channel.Write(out)
				_ = channel.CloseWrite()
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				if log != "" {
					_ = log
				}
				return
			}
		}()
	}
}

// A master installs the agent on a bare server through SSH: it uploads the
// binary, the identity it redeemed and a unit, starts it, and the agent joins
// the fleet and applies its spec.
func TestInstallNodeOverSSH(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)

	root := t.TempDir()
	host, port := startTestSSHD(t, root)
	// the server already has the core, as a provisioning step would leave it
	xrayPath := filepath.Join(root, "usr/local/x-ui/xray-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(xrayPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xrayPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	masters := map[string]string{}
	for m := range masterHosts {
		masters[m] = "tls://" + publicName(m) + ":443"
	}
	body, _ := json.Marshal(map[string]any{
		"host": host, "port": port, "user": "root",
		"auth":    map[string]string{"kind": "password", "password": "labpass"},
		"node":    "ru4",
		"masters": masters,
		"dir":     filepath.Join(root, "etc/fbnode"),
		"bin":     filepath.Join(root, "usr/local/bin/fbnode"),
		"unit":    filepath.Join(root, "etc/systemd/system/fbnode.service"),
		"xrayBin": xrayPath,
		"wait":    "20s",
	})
	resp, err := http.Post("http://"+ipOf("fi1m")+":8080/provision/install", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("install: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Node          string `json:"node"`
		SHA256        string `json:"sha256"`
		ServiceActive bool   `json:"serviceActive"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Node != "ru4" || !out.ServiceActive || out.SHA256 == "" {
		t.Fatalf("result %v %s", err, raw)
	}

	// the agent the installer started joins the fleet once it has a spec
	ru4 := nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{{
		Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-install", Host: "ru.lab.firewalk.test",
		Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
	}}}
	f.putSpec(t, "ru2", ru4)

	logPath := filepath.Join(root, "service.log")
	eventually(t, 90*time.Second, "the installed agent applies its spec", func() (bool, string) {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return false, err.Error()
		}
		return strings.Contains(string(b), "applying bundle"), tailOf(string(b), 4)
	})
}

func tailOf(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
