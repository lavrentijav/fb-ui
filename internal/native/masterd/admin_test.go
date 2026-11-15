package masterd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicAdminPathKeepsCredentialsPathsOpen(t *testing.T) {
	open := []string{
		"/healthz", "/ready", "/enroll", "/enroll/tokens", "/master/join", "/sub/abc", "/panel/", "/panel/api/webhooks/list",
		"/api/v1/ui/login", "/api/v1/sub/login",
	}
	for _, p := range open {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if !PublicAdminPath(r) {
			t.Fatalf("%s should need no admin credential", p)
		}
	}
	closed := []string{"/", "/templates", "/hosts", "/sub", "/master/tokens", "/nodes/state", "/net/ips", "/enrollx", "/panelx"}
	for _, p := range closed {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if PublicAdminPath(r) {
			t.Fatalf("%s must require an admin credential", p)
		}
	}
}

func TestAdminAddressIsLoopbackByDefault(t *testing.T) {
	d := &Daemon{}
	addr, err := d.AdminAddress()
	if err != nil {
		t.Fatal(err)
	}
	if addr != DefaultAdminListen {
		t.Fatalf("default address %q", addr)
	}

	// an explicit public bind is refused until the operator says so
	d.cfg.AdminListen = "0.0.0.0:8080"
	if _, err := d.AdminAddress(); err == nil {
		t.Fatal("a public admin bind was accepted silently")
	}
	d.cfg.AdminAllowPublic = true
	if addr, err := d.AdminAddress(); err != nil || addr != "0.0.0.0:8080" {
		t.Fatalf("explicit public bind: %q %v", addr, err)
	}
}

func TestLoopbackHost(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"127.0.0.1:0":    true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		":8080":          false, // every interface
		"0.0.0.0:8080":   false,
		"10.0.0.1:8080":  false,
		"garbage":        false,
	} {
		if got := loopbackHost(addr); got != want {
			t.Fatalf("loopbackHost(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Serve the admin handler with no host attached: it must refuse, never fall
// through to the unguarded surface.
func TestAdminHandlerRefusesBeforeTheMasterIsReady(t *testing.T) {
	d := &Daemon{}
	rec := httptest.NewRecorder()
	d.AdminHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/templates", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready admin handler answered %d", rec.Code)
	}
}
