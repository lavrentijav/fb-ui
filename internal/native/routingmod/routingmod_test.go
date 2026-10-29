package routingmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routingmod"
)

type env struct {
	h     *modtest.Host
	api   http.Handler
	admin string
	mod   *routingmod.Module
	now   time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{h: modtest.NewHost("m1"), mod: routingmod.New(), now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	e.h.Clock = func() time.Time { return e.now }
	e.admin, _ = apiauth.Create(context.Background(), e.h, "root", module.ScopeAdmin, 0)
	e.api = apiserver.New(e.h, []module.Module{e.mod})
	return e
}

func (e *env) do(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/api/v1/routing"+path, &buf)
	req.Header.Set("Authorization", "Bearer "+e.admin)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (e *env) list(t *testing.T, body map[string]any) string {
	t.Helper()
	rec, out := e.do("POST", "/lists", body)
	if rec.Code != 200 {
		t.Fatalf("create list = %d %s", rec.Code, rec.Body)
	}
	return out["id"].(string)
}

func TestListCreateValidatesAndTidiesEntries(t *testing.T) {
	e := setup(t)
	if rec, _ := e.do("POST", "/lists", map[string]any{"name": "x", "kind": "domain", "entries": []string{"bad entry"}}); rec.Code != 400 {
		t.Fatalf("a bad entry = %d", rec.Code)
	}
	id := e.list(t, map[string]any{"name": "ru-asn", "kind": "asn", "entries": []string{"13335", "AS13335", "as47541", ""}})
	row, _ := e.h.Get(routingmod.ListTable, id)
	if string(row["entries"]) != `["AS13335","AS47541"]` {
		t.Fatalf("entries = %s", row["entries"])
	}
}

func TestRuleNeedsExistingListsAndAKnownAction(t *testing.T) {
	e := setup(t)
	lid := e.list(t, map[string]any{"name": "ads", "kind": "domain", "entries": []string{"geosite:category-ads-all"}})
	for name, body := range map[string]map[string]any{
		"no lists":       {"name": "r", "action": "block"},
		"unknown list":   {"name": "r", "action": "block", "lists": []string{"nope"}},
		"unknown action": {"name": "r", "action": "explode", "lists": []string{lid}},
	} {
		if rec, _ := e.do("POST", "/rules", body); rec.Code != 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	if rec, _ := e.do("POST", "/rules", map[string]any{"name": "r", "action": "proxy_de", "lists": []string{lid}}); rec.Code != 200 {
		t.Fatalf("a valid rule = %d", rec.Code)
	}
}

func TestDeleteListIsRefusedWhileARuleUsesIt(t *testing.T) {
	e := setup(t)
	lid := e.list(t, map[string]any{"name": "ads", "kind": "domain", "entries": []string{"a.example"}})
	_, rule := e.do("POST", "/rules", map[string]any{"name": "r", "action": "block", "lists": []string{lid}})
	if rec, _ := e.do("DELETE", "/lists/"+lid, nil); rec.Code != 409 {
		t.Fatalf("delete used list = %d", rec.Code)
	}
	e.do("DELETE", "/rules/"+rule["id"].(string), nil)
	if rec, _ := e.do("DELETE", "/lists/"+lid, nil); rec.Code != 200 {
		t.Fatalf("delete free list = %d", rec.Code)
	}
}

func TestTesterUsesTheInboundsDefaultRoute(t *testing.T) {
	e := setup(t)
	lid := e.list(t, map[string]any{"name": "ru", "kind": "domain", "entries": []string{`regexp:\.ru$`}})
	e.do("POST", "/rules", map[string]any{"name": "RU direct", "action": "direct", "lists": []string{lid}})
	spec := `{"node":"ru4","inbounds":[{"tag":"de","route":"proxy_de"}]}`
	if _, err := e.h.Set(context.Background(), "nodespec", "ru4", map[string]any{"spec": json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	_, hit := e.do("POST", "/test", map[string]any{"node": "ru4", "inbound": "de", "target": "mail.yandex.ru"})
	if hit["route"] != "direct" || hit["rule"] != "RU direct" {
		t.Fatalf("hit = %v", hit)
	}
	_, miss := e.do("POST", "/test", map[string]any{"node": "ru4", "inbound": "de", "target": "example.com"})
	if miss["route"] != "proxy_de" || miss["default"] != true {
		t.Fatalf("miss = %v", miss)
	}
}

func TestOnlyTheLeaderResolvesAnASNAndAFailureKeepsThePrefixes(t *testing.T) {
	e := setup(t)
	fail := false
	e.mod.Fetch = func(_ context.Context, url string) ([]byte, error) {
		if fail {
			return nil, errors.New("HTTP 500")
		}
		return []byte(`{"data":{"prefixes":[{"prefix":"213.180.192.0/19"}]}}`), nil
	}
	id := e.list(t, map[string]any{"name": "ya", "kind": "asn", "entries": []string{"AS13238"}, "refreshHours": 24})

	e.h.IsLeader = false
	if n, _ := e.mod.Tick(context.Background(), e.h); n != 0 {
		t.Fatalf("a follower refreshed %d lists", n)
	}
	e.h.IsLeader = true
	if n, err := e.mod.Tick(context.Background(), e.h); n != 1 || err != nil {
		t.Fatalf("leader refreshed %d, err %v", n, err)
	}
	row, _ := e.h.Get(routingmod.ListTable, id)
	if string(row["remote"]) != `["213.180.192.0/19"]` {
		t.Fatalf("remote = %s", row["remote"])
	}
	if n, _ := e.mod.Tick(context.Background(), e.h); n != 0 {
		t.Fatal("a fresh list must not be fetched again")
	}
	e.now = e.now.Add(25 * time.Hour)
	fail = true
	if _, err := e.mod.Tick(context.Background(), e.h); err == nil {
		t.Fatal("a failing lookup must surface")
	}
	row, _ = e.h.Get(routingmod.ListTable, id)
	if string(row["remote"]) != `["213.180.192.0/19"]` || !strings.Contains(string(row["fetchError"]), "HTTP 500") {
		t.Fatalf("the old prefixes must stay and the error be recorded: %v", row)
	}
}

func TestLoadFeedsCompile(t *testing.T) {
	e := setup(t)
	lid := e.list(t, map[string]any{"name": "ads", "kind": "domain", "entries": []string{"ads.example"}})
	e.do("POST", "/rules", map[string]any{"name": "block", "action": "block", "lists": []string{lid}, "nodes": []string{"ru4"}})
	lists, rules := routingmod.Load(e.h)
	if len(lists) != 1 || len(rules) != 1 || rules[0].Lists[0] != lists[0].ID || rules[0].Nodes[0] != "ru4" {
		t.Fatalf("lists %+v rules %+v", lists, rules)
	}
}
