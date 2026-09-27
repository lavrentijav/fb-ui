package edge

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestReadHelloFromARealClient(t *testing.T) {
	cases := []struct {
		sni  string
		alpn []string
	}{
		{"panel.example.com", []string{"h2", "http/1.1"}},
		{"", nil},
	}
	for _, tc := range cases {
		cli, srv := net.Pipe()
		go func() {
			c := tls.Client(cli, &tls.Config{ServerName: tc.sni, NextProtos: tc.alpn, InsecureSkipVerify: true}) //nolint:gosec
			_ = c.Handshake()
		}()
		h, raw, err := readHello(srv, srv.SetReadDeadline, 5*time.Second)
		_ = srv.Close()
		if err != nil {
			t.Fatalf("sni %q: %v", tc.sni, err)
		}
		if h.SNI != tc.sni || !slices.Equal(h.ALPN, tc.alpn) || raw[0] != 0x16 {
			t.Fatalf("got sni=%q alpn=%v, want %q %v", h.SNI, h.ALPN, tc.sni, tc.alpn)
		}
	}

	if _, raw, err := readHello(strings.NewReader("GET / HTTP/1.1\r\n\r\n"), nil, 0); !errors.Is(err, errNotTLS) || string(raw) != "GET /" {
		t.Fatalf("plain HTTP: err=%v raw=%q", err, raw)
	}
}

func TestPickPrefersTheMostSpecificRoute(t *testing.T) {
	cfg := Config{
		Routes: []Route{
			{Match: "*.example.com", Target: "wild"},
			{Match: "*.a.example.com", Target: "deeper"},
			{Match: "x.a.example.com", Target: "exact"},
			{Match: "", Target: "nosni"},
		},
		Default: Route{Target: "default"},
	}
	for sni, want := range map[string]string{
		"x.a.example.com": "exact", "y.a.example.com": "deeper", "b.example.com": "wild",
		"example.com": "default", "other.org": "default", "": "nosni", "X.A.Example.COM.": "exact",
	} {
		if got := cfg.Pick(&Hello{SNI: sni}).Target; got != want {
			t.Errorf("Pick(%q) = %s, want %s", sni, got, want)
		}
	}
	if got := cfg.Pick(nil).Target; got != "default" {
		t.Errorf("non-TLS went to %s", got)
	}
}

// tlsEcho answers every TLS connection with its name and the first line it
// reads, proving the handshake passed through undecrypted.
func tlsEcho(t *testing.T, name string) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t, name)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "%s:%s", name, line)
			}()
		}
	}()
	return ln.Addr().String()
}

// proxyEcho reports the PROXY header it received, then the payload.
func proxyEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				hdr, _ := br.ReadString('\n')
				first := make([]byte, 5)
				_, _ = io.ReadFull(br, first)
				fmt.Fprintf(c, "%s|%q", strings.TrimSpace(hdr), first)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRouterDispatchesBySNI(t *testing.T) {
	siteAddr := tlsEcho(t, "site.example.com")
	defAddr := proxyEcho(t)

	panelLn := NewHandoffListener(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443})
	panelSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "panel sees %s", strings.Split(r.RemoteAddr, ":")[0])
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t, "*.panel.test")}},
	}
	go func() { _ = panelSrv.ServeTLS(panelLn, "", "") }()
	t.Cleanup(func() { _ = panelSrv.Close() })

	r := NewRouter(Config{
		Routes: []Route{
			{Match: "site.example.com", Action: ActionTCP, Target: siteAddr},
			{Match: "*.panel.test", Action: ActionHandoff, Target: "panel"},
			{Match: "blocked.example.com", Action: ActionReject},
		},
		Default: Route{Action: ActionTCP, Target: defAddr, ProxyProtocol: 1},
	}, map[string]*HandoffListener{"panel": panelLn})
	if err := r.cfg.Load().Validate([]string{"panel"}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = r.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

	c, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "site.example.com", InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	fmt.Fprint(c, "hello\n")
	got, _ := io.ReadAll(c)
	if cn := c.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "site.example.com" || string(got) != "site.example.com:hello\n" {
		t.Fatalf("site: cert %q body %q", cn, got)
	}

	pc, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "x.panel.test", InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		t.Fatalf("panel: %v", err)
	}
	fmt.Fprint(pc, "GET / HTTP/1.1\r\nHost: x.panel.test\r\nConnection: close\r\n\r\n")
	body, _ := io.ReadAll(pc)
	if !strings.Contains(string(body), "panel sees 127.0.0.1") {
		t.Fatalf("panel handoff: %q", body)
	}

	for _, sni := range []string{"unknown.example.org", ""} {
		raw, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		tc := tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true}) //nolint:gosec
		go func() { _ = tc.Handshake() }()
		_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, _ := io.ReadAll(raw)
		want := "PROXY TCP4 127.0.0.1 127.0.0.1 " + strings.Split(raw.LocalAddr().String(), ":")[1]
		if !strings.HasPrefix(string(resp), want) || !strings.Contains(string(resp), `|"\x16\x03`) {
			t.Fatalf("sni %q default route: %q, want PROXY header then the raw ClientHello", sni, resp)
		}
		_ = raw.Close()
	}

	plain, _ := net.Dial("tcp", addr)
	fmt.Fprint(plain, "GET / HTTP/1.1\r\n\r\n")
	_ = plain.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, _ := io.ReadAll(plain)
	if !strings.Contains(string(resp), `|"GET /"`) {
		t.Fatalf("non-TLS default route: %q", resp)
	}

	rc, _ := net.Dial("tcp", addr)
	rt := tls.Client(rc, &tls.Config{ServerName: "blocked.example.com", InsecureSkipVerify: true}) //nolint:gosec
	if err := rt.Handshake(); err == nil {
		t.Fatal("a rejected SNI completed a handshake")
	}
}

func TestProxyHeaderV2(t *testing.T) {
	src := &net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 51000}
	dst := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443}
	h := proxyHeader(2, src, dst)
	want := append(append([]byte{}, proxyV2Sig...), 0x21, 0x11, 0x00, 0x0C, 203, 0, 113, 7, 10, 0, 0, 1, 0xC7, 0x38, 0x01, 0xBB)
	if !slices.Equal(h, want) {
		t.Fatalf("v2 header = % x\nwant        % x", h, want)
	}
}

func TestHTTPRouterRoutesByHostAndPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			hj, _ := w.(http.Hijacker)
			c, brw, _ := hj.Hijack()
			defer c.Close()
			fmt.Fprint(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = brw.Flush()
			line, _ := brw.ReadString('\n')
			fmt.Fprintf(brw, "echo:%s", line)
			_ = brw.Flush()
			return
		}
		fmt.Fprintf(w, "backend %s %s xff=%s", r.Host, r.URL.Path, r.Header.Get("X-Forwarded-For"))
		if r.Header.Get(TrustHeader) == "" {
			fmt.Fprint(w, " untrusted")
		}
	}))
	t.Cleanup(backend.Close)
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "panel") })

	h := NewHTTPRouter(panel, nil)
	if err := h.Update([]HTTPRoute{
		{Host: "", PathPrefix: "/ws", Target: backend.URL},
		{Host: "site.example.com", PathPrefix: "/", Target: backend.URL},
		{Host: "*.example.com", PathPrefix: "/api/", Target: backend.URL + "/v1", StripPrefix: true},
	}); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)

	get := func(host, path string) string {
		req, _ := http.NewRequest(http.MethodGet, front.URL+path, nil)
		req.Host = host
		req.Header.Set("X-Forwarded-For", "6.6.6.6")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	cases := []struct{ host, path, want string }{
		{"panel.example.org", "/xui/panel", "panel"},
		{"panel.example.org", "/ws", "backend panel.example.org /ws xff=127.0.0.1"},
		{"panel.example.org", "/wsx", "panel"},
		{"site.example.com", "/anything", "backend site.example.com /anything xff=127.0.0.1"},
		{"a.example.com", "/api/users", "backend a.example.com /v1/users xff=127.0.0.1"},
		{"a.example.com", "/other", "panel"},
	}
	for _, tc := range cases {
		if got := get(tc.host, tc.path); got != tc.want {
			t.Errorf("%s%s = %q, want %q", tc.host, tc.path, got, tc.want)
		}
	}

	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET /ws/tunnel HTTP/1.1\r\nHost: p.example.org\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: x3JJHMbDL1EzLkh9GBhXDw==\r\n\r\n")
	br := bufio.NewReader(c)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade status %q", status)
	}
	for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
	}
	fmt.Fprint(c, "ping\n")
	echo, _ := br.ReadString('\n')
	if echo != "echo:ping\n" {
		t.Fatalf("websocket tunnel: %q", echo)
	}

	if err := h.Update([]HTTPRoute{{PathPrefix: "ws", Target: backend.URL}}); err == nil {
		t.Fatal("a path without a leading slash must be refused")
	}
	if got := get("site.example.com", "/"); got != "backend site.example.com / xff=127.0.0.1" {
		t.Fatalf("a refused update replaced the routes: %q", got)
	}
}

func TestHTTPRouterOutlivesTheServerTimeouts(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := range 4 {
			fmt.Fprintf(w, "chunk%d\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	t.Cleanup(backend.Close)
	h := NewHTTPRouter(http.NotFoundHandler(), nil)
	if err := h.Update([]HTTPRoute{{PathPrefix: "/stream", Target: backend.URL}}); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(h)
	front.Config.WriteTimeout = 200 * time.Millisecond
	front.Config.ReadTimeout = 200 * time.Millisecond
	front.Start()
	t.Cleanup(front.Close)

	resp, err := http.Get(front.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || strings.Count(string(body), "chunk") != 4 {
		t.Fatalf("stream cut by the server timeout: %q, %v", body, err)
	}
}
