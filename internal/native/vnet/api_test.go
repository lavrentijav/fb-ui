package vnet_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func call(t *testing.T, h http.Handler, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAPIFlow(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	s := st["fi1"]
	a, _ := s.CreateUser(ctx, "alice")
	b, _ := s.CreateUser(ctx, "bob")
	h := vnet.API(s, func() map[string]string { return map[string]string{"alice": "ru4"} })

	if code, _ := call(t, h, "", "GET", "/api/v1/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, me := call(t, h, a.Token, "GET", "/api/v1/me", ""); code != 200 || me["ip"] != a.IP || me["name"] != "alice" {
		t.Fatalf("me: %d %v", code, me)
	}
	if code, n := call(t, h, a.Token, "PUT", "/api/v1/me/name", `{"name":"alice-pc"}`); code != 200 || n["name"] != "alice-pc" {
		t.Fatalf("rename: %d %v", code, n)
	}
	if code, _ := call(t, h, b.Token, "PUT", "/api/v1/me/name", `{"name":"alice-pc"}`); code != http.StatusConflict {
		t.Fatalf("a taken name: %d", code)
	}
	if code, _ := call(t, h, b.Token, "PUT", "/api/v1/me/name", `{"name":"Bob!"}`); code != http.StatusBadRequest {
		t.Fatalf("a bad name: %d", code)
	}
	if code, _ := call(t, h, a.Token, "POST", "/api/v1/network", `{"title":"друзья"}`); code != 200 {
		t.Fatalf("create: %d", code)
	}
	code, inv := call(t, h, a.Token, "POST", "/api/v1/network/invites", `{"ttl":"1h","uses":1}`)
	if code != 200 || inv["code"] == "" {
		t.Fatalf("invite: %d %v", code, inv)
	}
	if code, _ := call(t, h, b.Token, "POST", "/api/v1/network/join", `{"code":"`+inv["code"].(string)+`"}`); code != 200 {
		t.Fatalf("join: %d", code)
	}
	code, net := call(t, h, b.Token, "GET", "/api/v1/network", "")
	members, _ := net["members"].([]any)
	if code != 200 || len(members) != 2 {
		t.Fatalf("network: %d %v", code, net)
	}
	online := 0
	for _, m := range members {
		if m.(map[string]any)["node"] == "ru4" {
			online++
		}
	}
	if online != 1 {
		t.Fatalf("presence not shown: %v", members)
	}
	if code, e := call(t, h, b.Token, "POST", "/api/v1/network", `{"title":"вторая"}`); code != http.StatusConflict || e["error"] != vnet.ErrInNetwork.Error() {
		t.Fatalf("second network: %d %v", code, e)
	}
}
