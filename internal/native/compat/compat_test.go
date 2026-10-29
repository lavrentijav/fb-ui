package compat_test

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
	"github.com/mhsanaei/3x-ui/v3/internal/native/auditmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/compat"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhookmod"
)

type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

type env struct {
	h     http.Handler
	admin string
}

func setup(t *testing.T) *env {
	t.Helper()
	host := modtest.NewHost("m1")
	wh := webhookmod.New()
	wh.CheckURL = func(string) error { return nil }
	wh.Sender.Sleep = func(time.Duration) {}
	mods := []module.Module{auditmod.New(), wh}
	api := apiserver.New(host, mods)
	admin, _ := apiauth.Create(context.Background(), host, "root", module.ScopeAdmin, 0)
	return &env{h: compat.Handler(mods, api), admin: admin}
}

func (e *env) do(method, path, token string, body any) (*httptest.ResponseRecorder, envelope) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var env envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

func TestLegacyListIsWrappedInTheOldEnvelope(t *testing.T) {
	e := setup(t)
	rec, env := e.do("POST", "/panel/api/webhooks/add", e.admin, map[string]any{"name": "a", "url": "https://h.example/x", "secret": "s", "enable": true})
	if rec.Code != 200 || !env.Success {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	rec, env = e.do("GET", "/panel/api/webhooks/list", e.admin, nil)
	var list []map[string]any
	if rec.Code != 200 || !env.Success || json.Unmarshal(env.Obj, &list) != nil || len(list) != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if list[0]["name"] != "a" || list[0]["hasSecret"] != true {
		t.Fatalf("item = %v", list[0])
	}
}

func TestLegacyErrorKeepsHTTP200AndSuccessFalse(t *testing.T) {
	e := setup(t)
	rec, env := e.do("POST", "/panel/api/webhooks/add", e.admin, map[string]any{"name": "", "url": "https://h.example/x"})
	if rec.Code != 200 || env.Success || env.Msg != "a name is required" {
		t.Fatalf("got %d %+v", rec.Code, env)
	}
}

func TestLegacyRoutesNeedTheSameToken(t *testing.T) {
	e := setup(t)
	if rec, _ := e.do("GET", "/panel/api/webhooks/list", "", nil); rec.Code != 401 {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec, _ := e.do("GET", "/panel/api/audit/list", "wrong", nil); rec.Code != 401 {
		t.Fatalf("bad token = %d", rec.Code)
	}
}

func TestLegacyPathParamsReachTheNewRoute(t *testing.T) {
	e := setup(t)
	_, env := e.do("POST", "/panel/api/webhooks/add", e.admin, map[string]any{"name": "a", "url": "https://h.example/x"})
	var created map[string]any
	_ = json.Unmarshal(env.Obj, &created)
	id := created["id"].(string)

	_, env = e.do("POST", "/panel/api/webhooks/update/"+id, e.admin, map[string]any{"name": "renamed", "url": "https://h.example/y"})
	var updated map[string]any
	if !env.Success || json.Unmarshal(env.Obj, &updated) != nil || updated["name"] != "renamed" {
		t.Fatalf("update = %+v", env)
	}
	if _, env = e.do("POST", "/panel/api/webhooks/del/"+id, e.admin, nil); !env.Success {
		t.Fatalf("del = %+v", env)
	}
	if _, env = e.do("POST", "/panel/api/webhooks/del/"+id, e.admin, nil); env.Success {
		t.Fatal("deleting twice must fail")
	}
}

func TestLegacyAuditListSeesTheChangesMadeThroughIt(t *testing.T) {
	e := setup(t)
	e.do("POST", "/panel/api/webhooks/add", e.admin, map[string]any{"name": "a", "url": "https://h.example/x"})
	_, env := e.do("GET", "/panel/api/audit/list", e.admin, nil)
	var entries []map[string]any
	if !env.Success || json.Unmarshal(env.Obj, &entries) != nil || len(entries) != 1 || entries[0]["path"] != "/api/v1/webhooks" {
		t.Fatalf("audit = %+v", env)
	}
}
