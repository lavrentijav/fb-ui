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
	"github.com/mhsanaei/3x-ui/v3/internal/native/compat"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subsmod"
)

type env struct {
	h    *modtest.Host
	api  http.Handler
	auth string
}

func setup(t *testing.T) *env {
	t.Helper()
	h := modtest.NewHost("m1")
	mod := subsmod.New()
	auth, _ := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	return &env{h: h, api: compat.Handler([]module.Module{mod}, apiserver.New(h, []module.Module{mod})), auth: auth}
}

func (e *env) post(t *testing.T, path string, body any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Authorization", "Bearer "+e.auth)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %s", path, rec.Body)
	}
	return out
}

func (e *env) get(t *testing.T, path string) any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+e.auth)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
	}
	var out struct {
		Success bool `json:"success"`
		Obj     any  `json:"obj"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Success {
		t.Fatalf("%s: %s", path, rec.Body)
	}
	return out.Obj
}

// The panel's subscription page calls /panel/api/subscriptions/*; a native
// master answers those through the module, with the generated password shown
// once and the login kept.
func TestSubscriptionPageCallsWorkOnThePanelSurface(t *testing.T) {
	e := setup(t)
	// one client with a link so the list can count it
	ctx := context.Background()
	if _, err := e.h.Set(ctx, "nodespec", "ru4", map[string]any{"spec": nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: "ru.example",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: "uuid-alice"}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	out := e.post(t, "/panel/api/subscriptions/create", map[string]any{"email": "alice", "user": "alice01"})
	if out["success"] != true {
		t.Fatalf("create: %v", out)
	}
	obj := out["obj"].(map[string]any)
	if obj["password"] == nil || obj["password"] == "" {
		t.Fatalf("a generated password should be shown once: %v", obj)
	}
	token := obj["token"].(string)

	list, _ := e.get(t, "/panel/api/subscriptions/list").([]any)
	if len(list) != 1 {
		t.Fatalf("list: %#v", list)
	}
	row := list[0].(map[string]any)
	if row["email"] != "alice" || row["user"] != "alice01" || row["links"] != float64(1) {
		t.Fatalf("row: %v", row)
	}
	if row["password"] != nil {
		t.Fatalf("the password must not be listed: %v", row)
	}

	// updating the login keeps the same token and does not mint a password
	out = e.post(t, "/panel/api/subscriptions/create", map[string]any{"email": "alice", "user": "alice02"})
	again := out["obj"].(map[string]any)
	if again["token"] != token || again["password"] != nil || again["user"] != "alice02" {
		t.Fatalf("update: %v", again)
	}

	// reset answers a new password once
	out = e.post(t, "/panel/api/subscriptions/password/alice", nil)
	if out["success"] != true || out["obj"].(map[string]any)["password"] == "" {
		t.Fatalf("reset: %v", out)
	}

	// disabling then enabling the link list
	if out := e.post(t, "/panel/api/subscriptions/enable/"+token, map[string]any{"enable": false}); out["success"] != true {
		t.Fatalf("disable: %v", out)
	}
	list, _ = e.get(t, "/panel/api/subscriptions/list").([]any)
	if list[0].(map[string]any)["enable"] != false {
		t.Fatalf("still enabled: %#v", list)
	}
	e.post(t, "/panel/api/subscriptions/enable/"+token, map[string]any{"enable": true})

	// deleting removes it
	if out := e.post(t, "/panel/api/subscriptions/del/"+token, nil); out["success"] != true {
		t.Fatalf("delete: %v", out)
	}
	if list, _ := e.get(t, "/panel/api/subscriptions/list").([]any); len(list) != 0 {
		t.Fatalf("still there: %#v", list)
	}
}

func TestVirtualNetworksAreListedForTheOperator(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	seed := []struct {
		table, key string
		fields     map[string]any
	}{
		{"vnet", "id-net", map[string]any{"title": "friends", "owner": "alice"}},
		{"vnet_member", "alice", map[string]any{"network": "id-net", "role": "owner"}},
		{"vnet_member", "bob", map[string]any{"network": "id-net", "role": "member"}},
		{"vuser", "alice", map[string]any{"email": "alice", "ip": "100.100.1.1"}},
		{"vuser", "bob", map[string]any{"email": "bob", "ip": "100.100.1.2"}},
		{"uname", "alice", map[string]any{"user": "alice"}},
		{"uname", "bob", map[string]any{"user": "bob"}},
	}
	for _, row := range seed {
		if _, err := e.h.Set(ctx, row.table, row.key, row.fields); err != nil {
			t.Fatal(err)
		}
	}
	nets, _ := e.get(t, "/panel/api/vnets/list").([]any)
	if len(nets) != 1 {
		t.Fatalf("networks: %#v", nets)
	}
	net := nets[0].(map[string]any)
	if net["title"] != "friends" || net["owner"] != "alice" {
		t.Fatalf("network: %v", net)
	}
	members := net["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members: %#v", members)
	}
	first := members[0].(map[string]any)
	if first["user"] != "alice" || first["name"] != "alice" || first["ip"] != "100.100.1.1" || first["role"] != "owner" {
		t.Fatalf("member: %v", first)
	}
}
