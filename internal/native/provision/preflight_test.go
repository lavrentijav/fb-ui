package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseSampleProbe(t *testing.T) {
	raw := `@@os
NAME="Debian GNU/Linux"
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
ID=debian
@@uname
Linux
x86_64
@@uid
0
@@systemd
/usr/bin/systemctl
@@bins
/usr/local/x-ui/x-ui
@@listen
LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*
LISTEN 0 4096 [::]:443 [::]:*
@@disk
/dev/vda1 20641404 8000000 11600000 41% /
@@end
`
	r := parse(raw)
	if r.OS != "Debian GNU/Linux 12 (bookworm)" || r.OSID != "debian" || r.Arch != "amd64" {
		t.Fatalf("os %+v", r)
	}
	if !r.Root || !r.Systemd || !r.PanelOnHost || r.AgentOnHost {
		t.Fatalf("flags %+v", r)
	}
	if !r.PortsFree.HTTP || r.PortsFree.HTTPS || !r.PortsFree.Alt {
		t.Fatalf("ports %+v (listeners %v)", r.PortsFree, r.Listeners)
	}
	if r.DiskFree != 11600000*1024 {
		t.Fatalf("disk %d", r.DiskFree)
	}
	if !strings.Contains(strings.Join(r.Warnings, ";"), "443") {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

func TestListenPort(t *testing.T) {
	for line, want := range map[string]int{
		"LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*":      22,
		"LISTEN 0 4096 [::]:443 [::]:*":           443,
		"tcp 0 0 127.0.0.1:2053 0.0.0.0:* LISTEN": 2053,
		"tcp6 0 0 :::80 :::* LISTEN":              80,
	} {
		got, ok := listenPort(line)
		if !ok || got != want {
			t.Fatalf("%q -> %d %v", line, got, ok)
		}
	}
	if _, ok := listenPort("nonsense"); ok {
		t.Fatal("a non-listener line produced a port")
	}
}

// testSSH is a tiny sshd that runs the probe on this machine.
type testSSH struct {
	host string
	port int
	pub  ssh.PublicKey
}

func startSSH(t *testing.T) *testSSH {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "root" && string(pass) == "s3cret" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
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
			go serveTestSSH(c, cfg)
		}
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &testSSH{host: host, port: port, pub: signer.PublicKey()}
}

func serveTestSSH(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
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
				out, _ := exec.Command("sh", "-c", payload.Command).Output()
				_, _ = channel.Write(out)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

func TestPreflightOverSSH(t *testing.T) {
	srv := startSSH(t)
	target := Target{Host: srv.host, Port: srv.port, User: "root"}
	good := Auth{Kind: "password", Password: "s3cret"}

	res, err := Preflight(context.Background(), target, good, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.HostKey != ssh.FingerprintSHA256(srv.pub) {
		t.Fatalf("host key %q, want %q", res.HostKey, ssh.FingerprintSHA256(srv.pub))
	}
	if res.Arch == "" {
		t.Fatalf("facts %+v", res)
	}

	if _, err := Preflight(context.Background(), target, good, res.HostKey); err != nil {
		t.Fatalf("pinned key refused: %v", err)
	}
	if _, err := Preflight(context.Background(), target, good, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("a wrong pinned key was accepted")
	}
	if _, err := Preflight(context.Background(), target, Auth{Kind: "password", Password: "wrong"}, ""); err == nil {
		t.Fatal("a wrong password was accepted")
	}
}

func TestKeyAuthAndBadInput(t *testing.T) {
	if _, err := Preflight(context.Background(), Target{}, Auth{}, ""); err == nil {
		t.Fatal("an empty target was accepted")
	}
	if _, err := clientConfig("root", Auth{Kind: "key", Key: "not a key"}, ""); err == nil {
		t.Fatal("a garbage private key was accepted")
	}
	if _, err := clientConfig("root", Auth{Kind: "sudo"}, ""); err == nil {
		t.Fatal("an unknown auth kind was accepted")
	}
	blk := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")})
	if _, err := clientConfig("root", Auth{Kind: "key", Key: string(blk)}, ""); err == nil {
		t.Fatal("a bogus PEM was accepted")
	}
}
