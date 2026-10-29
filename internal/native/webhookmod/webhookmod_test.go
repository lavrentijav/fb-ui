package webhookmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhook"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhookmod"
)

type env struct {
	h     *modtest.Host
	api   http.Handler
	admin string
	mod   *webhookmod.Module
}

func setup(t *testing.T) *env {
	t.Helper()
	t.Cleanup(webhookmod.SetURLCheck(func(string) error { return nil }))
	e := &env{h: modtest.NewHost("m1"), mod: webhookmod.New()}
	e.mod.Sender = webhook.New(nil)
	e.mod.Sender.Sleep = func(time.Duration) {}
	e.admin, _ = apiauth.Create(context.Background(), e.h, "root", module.ScopeAdmin, 0)
	e.api = apiserver.New(e.h, []module.Module{e.mod})
	return e
}

func (e *env) do(method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+e.admin)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

func (e *env) create(t *testing.T, body map[string]any) string {
	t.Helper()
	rec := e.do("POST", "/api/v1/webhooks", body)
	if rec.Code != 200 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out["id"].(string)
}

func TestCreateNeverEchoesTheSecret(t *testing.T) {
	e := setup(t)
	id := e.create(t, map[string]any{"name": "a", "url": "https://hooks.example/x", "secret": "TOPSECRET", "enable": true})
	rec := e.do("GET", "/api/v1/webhooks", nil)
	if strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatalf("the secret leaked: %s", rec.Body)
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["id"] != id || list[0]["hasSecret"] != true {
		t.Fatalf("list = %v", list)
	}
}

func TestUpdateWithEmptySecretKeepsStored(t *testing.T) {
	e := setup(t)
	id := e.create(t, map[string]any{"name": "a", "url": "https://hooks.example/x", "secret": "keep-me"})
	rec := e.do("PUT", "/api/v1/webhooks/"+id, map[string]any{"name": "b", "url": "https://hooks.example/y", "enable": true})
	if rec.Code != 200 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	row, _ := e.h.Get("webhook", id)
	if string(row["secret"]) != `"keep-me"` || string(row["name"]) != `"b"` {
		t.Fatalf("row = %v", row)
	}
}

func TestCreateRefusesAPrivateAddress(t *testing.T) {
	e := setup(t)
	restore := webhookmod.SetURLCheck(webhook.CheckPublicURL)
	defer restore()
	rec := e.do("POST", "/api/v1/webhooks", map[string]any{"name": "a", "url": "http://169.254.169.254/x"})
	if rec.Code != 400 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
}

func TestEmptyEventsMeansAll(t *testing.T) {
	if !webhookmod.Matches(nil, "node.down") || !webhookmod.Matches([]string{"node.down"}, "node.down") || webhookmod.Matches([]string{"cert.expiring"}, "node.down") {
		t.Fatal("event matching is wrong")
	}
}

func TestDeliverSignsAndRecordsStatus(t *testing.T) {
	e := setup(t)
	var mu sync.Mutex
	var hdr http.Header
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hdr = r.Header.Clone()
		body = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
	}))
	defer srv.Close()
	id := e.create(t, map[string]any{"name": "a", "url": srv.URL, "secret": "s3", "enable": true})
	ev := webhook.Event{Type: "node.down", Source: "ru4", Time: time.Unix(5, 0)}
	if err := e.mod.Deliver(context.Background(), e.h, id, ev, "m1:0001"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	ts := hdr.Get("X-Fbui-Timestamp")
	var n int64
	for _, c := range ts {
		n = n*10 + int64(c-'0')
	}
	if hdr.Get("X-Fbui-Delivery") != "m1:0001" || hdr.Get("X-Fbui-Event") != "node.down" ||
		hdr.Get("X-Fbui-Signature") != webhook.Sign("s3", n, body) {
		t.Fatalf("headers = %v", hdr)
	}
	st, _ := e.h.Get("webhook_status", id)
	if string(st["ok"]) != "true" {
		t.Fatalf("status = %v", st)
	}
}

func TestTestEndpointReportsTheReceiversAnswer(t *testing.T) {
	e := setup(t)
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	defer srv.Close()
	id := e.create(t, map[string]any{"name": "a", "url": srv.URL})
	if got := e.do("POST", "/api/v1/webhooks/"+id+"/test", nil).Code; got != 200 {
		t.Fatalf("test ok = %d", got)
	}
	code = http.StatusInternalServerError
	if got := e.do("POST", "/api/v1/webhooks/"+id+"/test", nil).Code; got != 502 {
		t.Fatalf("test failing = %d", got)
	}
	if got := e.do("DELETE", "/api/v1/webhooks/"+id, nil).Code; got != 200 {
		t.Fatalf("delete = %d", got)
	}
	if _, ok := e.h.Get("webhook", id); ok {
		t.Fatal("deleted webhook still there")
	}
}
