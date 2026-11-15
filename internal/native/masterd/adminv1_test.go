package masterd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The admin surface must hand /api/v1 to the module API (login and access
// pages live there) and keep the subscribers' /me and /network routes.
func TestAdminV1RoutesModulesAndSubscribers(t *testing.T) {
	d := &Daemon{}
	d.apiHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ui/login" {
			t.Errorf("module API got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	d.adminV1(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ui/login", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login through the admin surface: %d", rec.Code)
	}

	// /api/v1/me belongs to the subscriber API, which answers 503 while the
	// vnet store is not ready rather than falling through to a module 404.
	rec = httptest.NewRecorder()
	d.adminV1(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/v1/me was handled by the module API: %d", rec.Code)
	}
}
