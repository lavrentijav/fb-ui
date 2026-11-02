package compat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/compat"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// mapped is a module that answers one old route.
type mapped struct{}

func (mapped) Name() string                           { return "t" }
func (mapped) Tables() []string                       { return nil }
func (mapped) Mount(module.Router, module.Host)       {}
func (mapped) Run(ctx context.Context, _ module.Host) {}
func (mapped) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{{Method: "GET", Old: "/panel/api/known/list", New: "/api/v1/t"}}
}

var _ = context.Background

func TestAnUnmappedRouteExplainsItself(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	h := compat.Handler([]module.Module{mapped{}}, api)

	// the mapped route still reaches the API
	req := httptest.NewRequest("GET", "/panel/api/known/list", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("mapped route: %d %s", rec.Code, rec.Body)
	}

	// an unmapped one answers the old envelope with a readable refusal
	req = httptest.NewRequest("GET", "/panel/api/server/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unmapped route status %d", rec.Code)
	}
	var out struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Success || !strings.Contains(out.Msg, "not available on a native master") {
		t.Fatalf("refusal %+v", out)
	}
}
