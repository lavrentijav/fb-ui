package templatesmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templates"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templatesmod"
)

func spec() nodespec.Spec {
	return nodespec.Spec{
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: templates.NodeDomain, Route: nodespec.Direct,
		}},
		DirectAllow: []string{"10.0.0.0/16"},
	}
}

func call(t *testing.T, api http.Handler, token, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func setup(t *testing.T) (http.Handler, *modtest.Host, string) {
	t.Helper()
	h := modtest.NewHost("m1")
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	token, err := apiauth.Create(context.Background(), h, "ops", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	return apiserver.New(h, []module.Module{templatesmod.New()}), h, token
}

func TestCrudAndApply(t *testing.T) {
	api, h, token := setup(t)
	body, _ := json.Marshal(map[string]any{"name": "entry", "remark": "RU", "spec": spec()})
	if code, b := call(t, api, token, "POST", "/api/v1/templates", string(body)); code != 200 {
		t.Fatalf("create: %d %s", code, b)
	}
	if code, _ := call(t, api, token, "POST", "/api/v1/templates", string(body)); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	if code, b := call(t, api, token, "GET", "/api/v1/templates", ""); code != 200 || !bytes.Contains(b, []byte(`"entry"`)) {
		t.Fatalf("list: %d %s", code, b)
	}

	apply := `{"nodes":["ru7"],"domain":{"ru7":"ru7.walk.example"},"dryRun":true}`
	code, b := call(t, api, token, "POST", "/api/v1/templates/entry/apply", apply)
	if code != 200 || !bytes.Contains(b, []byte(`"applied":true`)) {
		t.Fatalf("dry run: %d %s", code, b)
	}
	if _, ok := h.Get(templatesmod.SpecTable, "ru7"); ok {
		t.Fatal("a dry run wrote a spec")
	}

	apply = `{"nodes":["ru7","de9"],"domain":{"ru7":"ru7.walk.example"}}`
	code, b = call(t, api, token, "POST", "/api/v1/templates/entry/apply", apply)
	if code != 200 {
		t.Fatalf("apply: %d %s", code, b)
	}
	var out []map[string]any
	_ = json.Unmarshal(b, &out)
	if len(out) != 2 || out[0]["applied"] != true {
		t.Fatalf("apply answer %s", b)
	}
	// de9 had no domain, so the placeholder is still there and it is refused
	if out[1]["applied"] != false || out[1]["missing"] == nil {
		t.Fatalf("de9 should be missing the domain: %s", b)
	}
	rec, ok := h.Get(templatesmod.SpecTable, "ru7")
	if !ok {
		t.Fatal("ru7 was not written")
	}
	var got nodespec.Spec
	_ = json.Unmarshal(rec["spec"], &got)
	if got.Node != "ru7" || got.Inbounds[0].Host != "ru7.walk.example" {
		t.Fatalf("written spec %+v", got)
	}

	if code, _ := call(t, api, token, "DELETE", "/api/v1/templates/entry", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(t, api, token, "GET", "/api/v1/templates/entry", ""); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestApplyNeedsNodesAndScope(t *testing.T) {
	api, h, admin := setup(t)
	body, _ := json.Marshal(map[string]any{"name": "entry", "spec": spec()})
	_, _ = call(t, api, admin, "POST", "/api/v1/templates", string(body))
	if code, _ := call(t, api, admin, "POST", "/api/v1/templates/entry/apply", `{"nodes":[]}`); code != http.StatusBadRequest {
		t.Fatalf("no nodes: %d", code)
	}
	mon, _ := apiauth.Create(context.Background(), h, "watch", module.ScopeMonitor, 0)
	if code, _ := call(t, api, mon, "POST", "/api/v1/templates/entry/apply", `{"nodes":["ru7"],"domain":{"ru7":"x"}}`); code != http.StatusForbidden {
		t.Fatalf("monitor applied: %d", code)
	}
}
