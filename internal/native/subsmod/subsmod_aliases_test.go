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

func TestSubscriptionAliasesAPI(t *testing.T) {
	h := modtest.NewHost("m1")
	auth, err := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Set(context.Background(), subauth.Table, "tok", map[string]any{"email": "alice", "enable": true}); err != nil {
		t.Fatal(err)
	}
	api := apiserver.New(h, []module.Module{subsmod.New()})
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
	aliases := func(body []byte) []string {
		t.Helper()
		var out struct {
			Aliases []string `json:"aliases"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		return out.Aliases
	}

	code, body := do("POST", "/api/v1/subs/tok/aliases", map[string]any{"alias": "old-sub-id"})
	if code != http.StatusOK {
		t.Fatalf("add alias: %d %s", code, body)
	}
	code, body = do("GET", "/api/v1/subs/tok/aliases", nil)
	if code != http.StatusOK || len(aliases(body)) != 1 || aliases(body)[0] != "old-sub-id" {
		t.Fatalf("list aliases: %d %s", code, body)
	}

	_, body = do("GET", "/api/v1/subs", nil)
	var rows []struct {
		Token   string   `json:"token"`
		Aliases []string `json:"aliases"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("subs: %v %s", err, body)
	}
	if len(rows) != 1 || len(rows[0].Aliases) != 1 || rows[0].Aliases[0] != "old-sub-id" {
		t.Fatalf("subs rows %+v", rows)
	}

	code, body = do("POST", "/api/v1/subs/tok/aliases", map[string]any{"alias": "tok"})
	if code != http.StatusConflict {
		t.Fatalf("primary as alias: %d %s", code, body)
	}

	code, body = do("DELETE", "/api/v1/subs/tok/aliases/old-sub-id", nil)
	if code != http.StatusOK {
		t.Fatalf("delete alias: %d %s", code, body)
	}
	code, body = do("GET", "/api/v1/subs/tok/aliases", nil)
	if code != http.StatusOK || len(aliases(body)) != 0 {
		t.Fatalf("aliases after delete: %d %s", code, body)
	}
}
