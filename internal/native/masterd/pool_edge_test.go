package masterd

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

func TestPoolEdgeRoutesPanelAndSubscriptions(t *testing.T) {
	d := &Daemon{cfg: Config{
		Name: "ru2", PublicName: "ru2.firewalk.top",
		PoolName: "master.firewalk.top",
		SubNames: []string{"sub.firewalk.top", "ru.firewalk.top"},
		Reverses: map[string]string{"walk.firewalk.top": "http://127.0.0.1:55004"},
	}}
	e := d.poolEdge("http://panel", "http://subs")
	wantSNI := []nodespec.SNIRoute{
		{Match: "ru2.firewalk.top", Action: "tcp", Target: "127.0.0.1:0"},
		{Match: "master.firewalk.top", Action: "handoff", Target: nodespec.Handoff},
		{Match: "sub.firewalk.top", Action: "handoff", Target: nodespec.Handoff},
		{Match: "ru.firewalk.top", Action: "handoff", Target: nodespec.Handoff},
		{Match: "walk.firewalk.top", Action: "handoff", Target: nodespec.Handoff},
	}
	if !slices.Equal(e.SNI, wantSNI) {
		t.Fatalf("SNI %+v, want %+v", e.SNI, wantSNI)
	}
	wantHTTP := []nodespec.HTTPRoute{
		{Host: "master.firewalk.top", PathPrefix: "/", Target: "http://panel"},
		{Host: "sub.firewalk.top", PathPrefix: "/", Target: "http://subs"},
		{Host: "ru.firewalk.top", PathPrefix: "/", Target: "http://subs"},
		{Host: "walk.firewalk.top", PathPrefix: "/", Target: "http://127.0.0.1:55004"},
	}
	if !slices.Equal(e.HTTP, wantHTTP) {
		t.Fatalf("HTTP %+v, want %+v", e.HTTP, wantHTTP)
	}
}

func TestCertNamesAddReversesInOrder(t *testing.T) {
	d := &Daemon{cfg: Config{
		PoolName: "master.firewalk.top",
		SubNames: []string{"sub.firewalk.top"},
		Reverses: map[string]string{"walk.firewalk.top": "http://x", "bot.firewalk.top": "http://y"},
	}}
	if got, want := d.certNames(), []string{"master.firewalk.top", "sub.firewalk.top", "bot.firewalk.top", "walk.firewalk.top"}; !slices.Equal(got, want) {
		t.Fatalf("cert names %v, want %v", got, want)
	}
}

func TestSubMuxHidesThePanel(t *testing.T) {
	d := &Daemon{cfg: Config{Name: "ru2", UIDir: t.TempDir()}}
	mux := d.subMux()
	for _, path := range []string{"/healthz", "/sub/x", "/walksub/x", "/frvpn/x", "/api/v1/sub/me", "/panel/assets/app.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not routed on the subscription host", path)
		}
	}
	for _, path := range []string{"/", "/panel", "/panel/", "/panel/index.html", "/panel/api/setting"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Errorf("%s is routed as %q on the subscription host", path, pattern)
		}
	}
}

func TestPublicMuxKeepsTheOldSubscriptionPaths(t *testing.T) {
	d := &Daemon{cfg: Config{Name: "ru2", UIDir: t.TempDir()}}
	mux := d.publicMux()
	for _, path := range []string{"/sub/x", "/walksub/x", "/frvpn/x", "/panel/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not routed on the panel host", path)
		}
	}
}

func TestPoolNamesDedupes(t *testing.T) {
	d := &Daemon{cfg: Config{PoolName: "master.firewalk.top", SubNames: []string{"sub.firewalk.top", "master.firewalk.top", "sub.firewalk.top", ""}}}
	if got, want := d.poolNames(), []string{"master.firewalk.top", "sub.firewalk.top"}; !slices.Equal(got, want) {
		t.Fatalf("pool names %v, want %v", got, want)
	}
}
