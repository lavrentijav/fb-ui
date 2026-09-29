//go:build netlab

// Package netlab runs nodes on their own addresses in 10.0.0.0/16, each in a
// network namespace with 443, real names and certificates from a lab CA.
// Run it through tools/netlab/run.sh; it needs no privileges on the host.
package netlab

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// host is one machine of the lab: a namespace with one address.
type host struct {
	name  string
	ip    string
	names []string // DNS names pointing at it
}

var hosts = []host{
	{"ru4", "10.0.2.4", []string{"ru4.lab.firewalk.test", "ru.lab.firewalk.test"}},
	{"de2", "10.0.3.2", []string{"de2.lab.firewalk.test"}},
	{"web", "10.0.8.1", []string{"web.lab.test", "mail.lab.firewalk.test"}},
	{"client", "10.0.9.1", nil},
}

func ipOf(name string) string {
	for _, h := range hosts {
		if h.name == name {
			return h.ip
		}
	}
	panic(name)
}

func TestMain(m *testing.M) {
	switch os.Getenv("NETLAB_ROLE") {
	case "node":
		os.Exit(runNode())
	case "web":
		os.Exit(runWeb())
	}
	if os.Getenv("NETLAB") != "1" {
		fmt.Println("netlab: run through tools/netlab/run.sh")
		os.Exit(0)
	}
	if err := buildNetwork(); err != nil {
		fmt.Println("netlab:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func sh(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// buildNetwork makes the bridge 10.0.0.1/16 (the "internet"), one namespace
// per host plugged into it, and an /etc/hosts for the lab names.
func buildNetwork() error {
	steps := [][]string{
		{"mount", "-t", "tmpfs", "none", "/run"},
		{"mkdir", "-p", "/run/netns"},
		{"ip", "link", "set", "lo", "up"},
		{"ip", "link", "add", "br0", "type", "bridge"},
		{"ip", "addr", "add", "10.0.0.1/16", "dev", "br0"},
		{"ip", "link", "set", "br0", "up"},
	}
	hostsFile := "127.0.0.1 localhost\n"
	for i, h := range hosts {
		veth, peer := fmt.Sprintf("v%d", i), fmt.Sprintf("p%d", i)
		steps = append(steps,
			[]string{"ip", "netns", "add", h.name},
			[]string{"ip", "link", "add", veth, "type", "veth", "peer", "name", peer},
			[]string{"ip", "link", "set", peer, "netns", h.name},
			[]string{"ip", "link", "set", veth, "master", "br0", "up"},
			[]string{"ip", "-n", h.name, "addr", "add", h.ip + "/16", "dev", peer},
			[]string{"ip", "-n", h.name, "link", "set", peer, "up"},
			[]string{"ip", "-n", h.name, "link", "set", "lo", "up"},
		)
		for _, n := range h.names {
			hostsFile += h.ip + " " + n + "\n"
		}
	}
	for _, s := range steps {
		if err := sh(s...); err != nil {
			return err
		}
	}
	f := filepath.Join(os.TempDir(), "netlab-hosts")
	if err := os.WriteFile(f, []byte(hostsFile), 0o644); err != nil {
		return err
	}
	return sh("mount", "--bind", f, "/etc/hosts")
}

// spawn runs this test binary as role inside host's namespace.
func spawn(t *testing.T, hostName, role string, env ...string) {
	t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command("ip", "netns", "exec", hostName, self)
	cmd.Env = append(os.Environ(), append(env, "NETLAB_ROLE="+role)...)
	logf, _ := os.Create(filepath.Join(t.TempDir(), hostName+".log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("--- %s log:\n%s", hostName, b)
		}
	})
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never listened", addr)
}

// runNode is a node agent without masters: it applies one bundle file with
// the same code fbnode uses, then serves until SIGTERM.
func runNode() int {
	raw, err := os.ReadFile(os.Getenv("NETLAB_BUNDLE"))
	if err != nil {
		fmt.Println(err)
		return 1
	}
	dir := os.Getenv("NETLAB_DIR")
	xr := &xrayrun.Runner{Bin: os.Getenv("NETLAB_XRAY"), Config: filepath.Join(dir, "xray.json"), Output: os.Stdout}
	defer xr.Stop()
	edge := &edgerun.Runner{Listen: ":443", Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}
	defer edge.Close()
	b := &agent.BundleApplier{Xray: xr, Edge: edge}
	if err := b.Apply(context.Background(), desired.State{Config: raw}); err != nil {
		fmt.Println("apply:", err)
		return 1
	}
	fmt.Println("node up")
	waitSignal()
	return 0
}

// runWeb is a site on the internet: /ip answers with the caller's address;
// :8443 is a TLS service with its own certificate (for SNI splicing).
func runWeb() int {
	who := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		fmt.Fprintf(w, "%s %s", r.URL.Path, host)
	})
	go func() { _ = http.ListenAndServe(":80", who) }()
	pair, err := tls.LoadX509KeyPair(os.Getenv("NETLAB_CERT"), os.Getenv("NETLAB_KEY"))
	if err != nil {
		fmt.Println(err)
		return 1
	}
	ln, err := tls.Listen("tcp", ":8443", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.WriteString(c, "mail-backend\n"); _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	waitSignal()
	return 0
}

func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
	<-ch
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
