package subsmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subsmod"
)

func TestSubscriptionCustomParamsAPI(t *testing.T) {
	h := modtest.NewHost("m1")
	mod := subsmod.New()
	auth, err := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Set(context.Background(), subauth.Table, "tok", map[string]any{"email": "alice", "enable": true}); err != nil {
		t.Fatal(err)
	}
	api := apiserver.New(h, []module.Module{mod})
	do := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+auth)
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	params := func(body []byte) map[string]string {
		t.Helper()
		var out struct {
			Params map[string]string `json:"params"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		return out.Params
	}

	code, body := do("PUT", "/api/v1/subs/tok/params", map[string]any{
		"params": map[string]string{"flag": "on", "plan": "pro"},
	})
	if code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}
	if got := params(body); got["flag"] != "on" || got["plan"] != "pro" {
		t.Fatalf("put params %v", got)
	}

	code, body = do("POST", "/api/v1/subs/tok/params", map[string]any{"params": map[string]string{"flag": "off"}})
	if code != http.StatusOK {
		t.Fatalf("post: %d %s", code, body)
	}
	if got := params(body); got["flag"] != "off" || got["plan"] != "pro" {
		t.Fatalf("merge params %v", got)
	}

	code, body = do("DELETE", "/api/v1/subs/tok/params/plan", nil)
	if code != http.StatusOK {
		t.Fatalf("delete key: %d %s", code, body)
	}
	if got := params(body); got["plan"] != "" || got["flag"] != "off" {
		t.Fatalf("after key delete %v", got)
	}

	code, body = do("GET", "/api/v1/subs/tok/params", nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	if got := params(body); got["flag"] != "off" {
		t.Fatalf("get params %v", got)
	}

	code, body = do("DELETE", "/api/v1/subs/tok/params", nil)
	if code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	if got := params(body); len(got) != 0 {
		t.Fatalf("after delete %v", got)
	}

	code, _ = do("PUT", "/api/v1/subs/missing/params", map[string]any{"params": map[string]string{"x": "1"}})
	if code != http.StatusNotFound {
		t.Fatalf("missing token: %d", code)
	}
	code, _ = do("PUT", "/api/v1/subs/tok/params", map[string]any{"params": map[string]string{"bad key": "1"}})
	if code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", code)
	}
}
