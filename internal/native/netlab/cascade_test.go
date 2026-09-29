//go:build netlab

package netlab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const (
	aliceID   = "11111111-2222-4333-8444-555555555555"
	serversID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

type lab struct {
	dir   string
	ca    *x509.Certificate
	caKey *ecdsa.PrivateKey
	xray  string
}

// ECDSA like Let's Encrypt: clients with a Chrome fingerprint refuse Ed25519.
func newCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "netlab CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{dir: t.TempDir()}
	var err error
	if l.ca, l.caKey, err = newCA(); err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.ca.Raw})
	caFile := filepath.Join(l.dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	// every process in the lab trusts the lab CA as if it were public
	t.Setenv("SSL_CERT_FILE", caFile)
	// a copy drops the host binary's file capabilities, which a user
	// namespace cannot honor
	l.xray = filepath.Join(l.dir, "xray")
	src, err := os.ReadFile(os.Getenv("NETLAB_XRAY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.xray, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *lab) cert(t *testing.T, names ...string) nodespec.Cert {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, l.ca, &key.PublicKey, l.caKey)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := x509.MarshalPKCS8PrivateKey(key)
	return nodespec.Cert{
		Names:   names,
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})),
	}
}

// node renders spec and starts it in its namespace, waiting for :443.
func (l *lab) node(t *testing.T, spec nodespec.Spec) *exec.Cmd {
	t.Helper()
	b, err := nodespec.Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.dir, spec.Node)
	os.MkdirAll(dir, 0o700)
	writeJSON(t, filepath.Join(dir, "bundle.json"), b)
	cmd := spawn(t, spec.Node, "node", "NETLAB_BUNDLE="+filepath.Join(dir, "bundle.json"), "NETLAB_DIR="+dir, "NETLAB_XRAY="+l.xray)
	waitTCP(t, ipOf(spec.Node)+":443")
	return cmd
}

// client runs Xray in the client namespace: one SOCKS port per path of ru4.
func (l *lab) client(t *testing.T, paths map[int]string) {
	t.Helper()
	var inbounds, outbounds, rules []string
	for port, path := range paths {
		tag := fmt.Sprint(port)
		inbounds = append(inbounds, fmt.Sprintf(`{"tag":"in%s","listen":"%s","port":%d,"protocol":"socks","settings":{"udp":false}}`, tag, ipOf("client"), port))
		outbounds = append(outbounds, fmt.Sprintf(`{"tag":"out%s","protocol":"vless","settings":{"vnext":[{"address":"ru.lab.firewalk.test","port":443,"users":[{"id":"%s","encryption":"none"}]}]},
			"streamSettings":{"network":"ws","security":"tls","tlsSettings":{"serverName":"ru.lab.firewalk.test","alpn":["http/1.1"]},"wsSettings":{"path":"%s","host":"ru.lab.firewalk.test"}}}`, tag, aliceID, path))
		rules = append(rules, fmt.Sprintf(`{"type":"field","inboundTag":["in%s"],"outboundTag":"out%s"}`, tag, tag))
	}
	cfg := fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[%s],"outbounds":[%s],"routing":{"rules":[%s]}}`,
		strings.Join(inbounds, ","), strings.Join(outbounds, ","), strings.Join(rules, ","))
	f := filepath.Join(l.dir, "client.json")
	os.WriteFile(f, []byte(cfg), 0o600)
	cmd := exec.Command("ip", "netns", "exec", "client", l.xray, "run", "-c", f)
	out, _ := os.Create(filepath.Join(l.dir, "client.log"))
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(out.Name())
			t.Logf("--- client log:\n%s", b)
		}
	})
	for port := range paths {
		waitTCP(t, fmt.Sprintf("%s:%d", ipOf("client"), port))
	}
}

func getVia(socks int, target string) (string, error) {
	proxy, _ := url.Parse(fmt.Sprintf("socks5h://%s:%d", ipOf("client"), socks))
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	resp, err := c.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func stats(t *testing.T, l *lab, node string) string {
	t.Helper()
	out, err := exec.Command("ip", "netns", "exec", node, l.xray, "api", "statsquery", "--server=127.0.0.1:10085").CombinedOutput()
	if err != nil {
		t.Fatalf("%s stats: %v %s", node, err, out)
	}
	return string(out)
}

// Near-production path: client -> ru4:443 (edge, TLS by name, WS to Xray,
// proxy_de) -> de2:443 (edge, WS to Xray, direct) -> a site, every hop on
// its own address and every certificate verified.
func TestCascadeThroughEdges(t *testing.T) {
	l := newLab(t)
	webCert := l.cert(t, "mail.lab.firewalk.test")
	wdir := filepath.Join(l.dir, "web")
	os.MkdirAll(wdir, 0o700)
	os.WriteFile(filepath.Join(wdir, "cert.pem"), []byte(webCert.CertPEM), 0o600)
	os.WriteFile(filepath.Join(wdir, "key.pem"), []byte(webCert.KeyPEM), 0o600)
	spawn(t, "web", "web", "NETLAB_CERT="+filepath.Join(wdir, "cert.pem"), "NETLAB_KEY="+filepath.Join(wdir, "key.pem"))
	waitTCP(t, ipOf("web")+":80")

	l.node(t, nodespec.Spec{
		Node: "de2",
		Inbounds: []nodespec.Inbound{{
			Tag: "cascade", Port: 12443, Network: "ws", Path: "/ws-update", Host: "de2.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: serversID}},
		}},
		Certs:       []nodespec.Cert{l.cert(t, "de2.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	})
	l.node(t, nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.lab.firewalk.test", Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
			{Tag: "ru", Port: 12054, Network: "ws", Path: "/ws-ru", Host: "ru.lab.firewalk.test", Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
			{Tag: "blk", Port: 12055, Network: "ws", Path: "/ws-blk", Host: "ru.lab.firewalk.test", Route: nodespec.Block, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
		},
		Exits: []nodespec.Exit{{Country: "de", Hops: []nodespec.Hop{{
			Address: "de2.lab.firewalk.test", Port: 443, ID: serversID, Network: "ws", Path: "/ws-update",
			SNI: "de2.lab.firewalk.test", Host: "de2.lab.firewalk.test",
		}}}},
		Reverse: []nodespec.Reverse{
			{Kind: "http", SNI: "ru.lab.firewalk.test", Path: "/sub/", Target: "http://web.lab.test"},
			{Kind: "tcp", SNI: "mail.lab.firewalk.test", Target: ipOf("web") + ":8443"},
		},
		Certs:       []nodespec.Cert{l.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	})
	l.client(t, map[int]string{1080: "/ws-de", 1081: "/ws-ru", 1082: "/ws-blk"})

	t.Run("proxy_de leaves through de2", func(t *testing.T) {
		got, err := getVia(1080, "http://web.lab.test/ip")
		if err != nil || got != "/ip "+ipOf("de2") {
			t.Fatalf("got %q %v, want the site to see de2 (%s)", got, err, ipOf("de2"))
		}
	})
	t.Run("direct leaves from ru4", func(t *testing.T) {
		got, err := getVia(1081, "http://web.lab.test/ip")
		if err != nil || got != "/ip "+ipOf("ru4") {
			t.Fatalf("got %q %v, want ru4 (%s)", got, err, ipOf("ru4"))
		}
	})
	t.Run("block goes nowhere", func(t *testing.T) {
		if got, err := getVia(1082, "http://web.lab.test/ip"); err == nil {
			t.Fatalf("blocked route answered %q", got)
		}
	})
	t.Run("http reverse on the edge", func(t *testing.T) {
		resp, err := http.Get("https://ru.lab.firewalk.test/sub/abc")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "/sub/abc "+ipOf("ru4") {
			t.Fatalf("reverse answered %q", b)
		}
	})
	t.Run("tcp reverse splices TLS untouched", func(t *testing.T) {
		c, err := tls.Dial("tcp", ipOf("ru4")+":443", &tls.Config{ServerName: "mail.lab.firewalk.test"})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if cn := c.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "mail.lab.firewalk.test" {
			t.Fatalf("spliced to a server presenting %q", cn)
		}
	})
	t.Run("unknown name is refused", func(t *testing.T) {
		c, err := tls.Dial("tcp", ipOf("ru4")+":443", &tls.Config{ServerName: "nope.lab.firewalk.test", InsecureSkipVerify: true}) //nolint:gosec
		if err == nil {
			c.Close()
			t.Fatal("edge accepted an unknown SNI")
		}
	})
	t.Run("traffic is counted on both nodes", func(t *testing.T) {
		time.Sleep(500 * time.Millisecond)
		if s := stats(t, l, "ru4"); !strings.Contains(s, "user>>>alice>>>traffic") {
			t.Fatalf("ru4 stats: %s", s)
		}
		if s := stats(t, l, "de2"); !strings.Contains(s, "user>>>servers>>>traffic") {
			t.Fatalf("de2 stats: %s", s)
		}
	})
}
