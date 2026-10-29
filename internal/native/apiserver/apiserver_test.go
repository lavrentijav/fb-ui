package apiserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

type demo struct{}

func (demo) Name() string                     { return "demo" }
func (demo) Tables() []string                 { return nil }
func (demo) Legacy() []module.LegacyRoute     { return nil }
func (demo) Run(context.Context, module.Host) {}
func (demo) Mount(r module.Router, _ module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, 200, []string{"a"})
	})
	r.Handle("POST", "/{id}", module.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		name, _ := apiserver.Principal(r)
		apiserver.JSON(w, 200, map[string]string{"id": r.PathValue("id"), "by": name})
	})
}

func setup(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	h := modtest.NewHost("m1")
	admin, _ := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	mon, _ := apiauth.Create(context.Background(), h, "watch", module.ScopeMonitor, 0)
	return apiserver.New(h, []module.Module{demo{}}), admin, mon
}

func do(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutesRequireTokenAndScope(t *testing.T) {
	h, admin, mon := setup(t)
	for _, c := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/api/v1/demo", "", 401},
		{"GET", "/api/v1/demo", "bogus", 401},
		{"GET", "/api/v1/demo", mon, 200},
		{"POST", "/api/v1/demo/7", mon, 403},
		{"POST", "/api/v1/demo/7", admin, 200},
	} {
		if got := do(h, c.method, c.path, c.token).Code; got != c.want {
			t.Fatalf("%s %s: %d, want %d", c.method, c.path, got, c.want)
		}
	}
	var out map[string]string
	_ = json.Unmarshal(do(h, "POST", "/api/v1/demo/7", admin).Body.Bytes(), &out)
	if out["id"] != "7" || out["by"] != "root" {
		t.Fatalf("handler saw %v", out)
	}
}

func TestReadyNeedsNoToken(t *testing.T) {
	h, _, _ := setup(t)
	if got := do(h, "GET", "/api/v1/ready", "").Code; got != 200 {
		t.Fatalf("ready = %d", got)
	}
}

func TestOpenAPIListsMountedRoutes(t *testing.T) {
	h, admin, mon := setup(t)
	if got := do(h, "GET", "/api/v1/openapi.json", mon).Code; got != 403 {
		t.Fatalf("openapi for monitor = %d, want 403", got)
	}
	rec := do(h, "GET", "/api/v1/openapi.json", admin)
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Paths["/api/v1/demo"]["get"]; !ok {
		t.Fatalf("paths = %s", strings.TrimSpace(rec.Body.String()))
	}
	if _, ok := doc.Paths["/api/v1/demo/{id}"]["post"]; !ok {
		t.Fatalf("paths = %s", strings.TrimSpace(rec.Body.String()))
	}
}
