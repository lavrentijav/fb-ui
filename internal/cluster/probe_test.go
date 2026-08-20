package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func targetForServer(t *testing.T, srv *httptest.Server, allowPrivate bool) ProbeTarget {
	t.Helper()
	host, port, ok := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	if !ok {
		t.Fatalf("unexpected test server URL %q", srv.URL)
	}
	p := 0
	for _, r := range port {
		p = p*10 + int(r-'0')
	}
	return ProbeTarget{Scheme: "http", Domain: host, Port: p, SubPath: "/sub/", AllowPrivate: allowPrivate}
}

func TestProbeReadsIdentity(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"alg":"ed25519","key":"aabb"}`))
	}))
	defer srv.Close()

	res := Probe(context.Background(), NewProbeClient(), targetForServer(t, srv, true))
	if res.Err != nil {
		t.Fatalf("Probe: %v", res.Err)
	}
	if res.PublicKey != "aabb" {
		t.Fatalf("PublicKey = %q, want %q", res.PublicKey, "aabb")
	}
	if gotPath != "/sub/pubkey" {
		t.Fatalf("probed %q, want /sub/pubkey", gotPath)
	}
}

func TestProbeReportsHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	res := Probe(context.Background(), NewProbeClient(), targetForServer(t, srv, true))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "502") {
		t.Fatalf("Err = %v, want an error naming HTTP 502", res.Err)
	}
}

func TestProbeRejectsNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	}))
	defer srv.Close()

	res := Probe(context.Background(), NewProbeClient(), targetForServer(t, srv, true))
	if res.Err == nil {
		t.Fatal("Err = nil, want a parse error for a non-JSON body")
	}
}

// The SSRF guard is what stops a peer row from turning the panel into a probe
// of its own internal network, so it must reject loopback unless opted in.
func TestProbeBlocksPrivateAddressByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"alg":"ed25519","key":"aabb"}`))
	}))
	defer srv.Close()

	res := Probe(context.Background(), NewProbeClient(), targetForServer(t, srv, false))
	if res.Err == nil {
		t.Fatal("Err = nil, want the SSRF guard to refuse a loopback peer")
	}
}

func TestIdentityURL(t *testing.T) {
	tests := []struct {
		name   string
		target ProbeTarget
		want   string
	}{
		{
			name:   "defaults to https",
			target: ProbeTarget{Domain: "sub2.example.com", Port: 2096, SubPath: "/sub/"},
			want:   "https://sub2.example.com:2096/sub/pubkey",
		},
		{
			name:   "normalizes a path missing both slashes",
			target: ProbeTarget{Scheme: "http", Domain: "sub2.example.com", Port: 80, SubPath: "link"},
			want:   "http://sub2.example.com:80/link/pubkey",
		},
		{
			name:   "falls back to /sub/ when the path is empty",
			target: ProbeTarget{Scheme: "https", Domain: "sub2.example.com", Port: 443},
			want:   "https://sub2.example.com:443/sub/pubkey",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IdentityURL(tt.target)
			if err != nil {
				t.Fatalf("IdentityURL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("IdentityURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdentityURLRejectsBadPort(t *testing.T) {
	if _, err := IdentityURL(ProbeTarget{Domain: "sub2.example.com", Port: 0}); err == nil {
		t.Fatal("IdentityURL with port 0 returned no error")
	}
}
