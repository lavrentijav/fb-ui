package auditmod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/auditmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

type demo struct{}

func (demo) Name() string                     { return "demo" }
func (demo) Tables() []string                 { return nil }
func (demo) Legacy() []module.LegacyRoute     { return nil }
func (demo) Run(context.Context, module.Host) {}
func (demo) Mount(r module.Router, _ module.Host) {
	ok := func(w http.ResponseWriter, _ *http.Request) { apiserver.JSON(w, 200, "ok") }
	r.Handle("GET", "/read", module.ScopeMonitor, ok)
	r.Handle("POST", "/change/{id}", module.ScopeAdmin, ok)
}

type env struct {
	h          *modtest.Host
	api        http.Handler
	admin, mon string
	now        time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{h: modtest.NewHost("m1"), now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	e.h.Clock = func() time.Time { return e.now }
	ctx := context.Background()
	e.admin, _ = apiauth.Create(ctx, e.h, "root", module.ScopeAdmin, 0)
	e.mon, _ = apiauth.Create(ctx, e.h, "watch", module.ScopeMonitor, 0)
	e.api = apiserver.New(e.h, []module.Module{auditmod.New(), demo{}})
	return e
}

func (e *env) do(method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

func (e *env) list(t *testing.T, query string) []map[string]any {
	t.Helper()
	rec := e.do("GET", "/api/v1/audit"+query, e.mon)
	if rec.Code != 200 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMiddlewareRecordsChangesNotReads(t *testing.T) {
	e := setup(t)
	e.do("GET", "/api/v1/demo/read", e.mon)
	e.do("POST", "/api/v1/demo/change/9", e.admin)
	got := e.list(t, "")
	if len(got) != 1 {
		t.Fatalf("entries = %v", got)
	}
	g := got[0]
	if g["actor"] != "root" || g["method"] != "POST" || g["path"] != "/api/v1/demo/change/{id}" || g["status"] != float64(200) || g["ip"] != "203.0.113.7" {
		t.Fatalf("entry = %v", g)
	}
}

func TestFailedChangeIsRecordedWithItsStatus(t *testing.T) {
	e := setup(t)
	if code := e.do("POST", "/api/v1/demo/change/9", e.mon).Code; code != 403 {
		t.Fatalf("monitor changing = %d", code)
	}
	got := e.list(t, "")
	if len(got) != 1 || got[0]["status"] != float64(403) || got[0]["actor"] != "watch" {
		t.Fatalf("entries = %v", got)
	}
}

func TestListFiltersByActorAndPages(t *testing.T) {
	e := setup(t)
	for i := 0; i < 3; i++ {
		e.do("POST", "/api/v1/demo/change/1", e.admin)
	}
	e.do("POST", "/api/v1/demo/change/1", e.mon)
	if n := len(e.list(t, "?actor=watch")); n != 1 {
		t.Fatalf("actor filter = %d", n)
	}
	page := e.list(t, "?limit=2")
	if len(page) != 2 {
		t.Fatalf("limit = %d", len(page))
	}
	next := e.list(t, "?limit=10&before="+page[1]["id"].(string))
	if len(next) != 2 {
		t.Fatalf("second page = %d, want the 2 older entries", len(next))
	}
}

func TestPruneDropsOnlyOldEntries(t *testing.T) {
	e := setup(t)
	e.do("POST", "/api/v1/demo/change/1", e.admin)
	e.now = e.now.Add(100 * 24 * time.Hour)
	e.do("POST", "/api/v1/demo/change/2", e.admin)
	e.now = e.now.Add(81 * 24 * time.Hour) // the first is 181 days old, the second 81
	auditmod.Prune(context.Background(), e.h)
	if n := len(e.h.Keys(auditmod.Table)); n != 1 {
		t.Fatalf("entries left = %d, want 1", n)
	}
}
