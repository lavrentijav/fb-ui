package groupsmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/compat"
	"github.com/mhsanaei/3x-ui/v3/internal/native/groupsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

type env struct {
	h    *modtest.Host
	api  http.Handler // /panel/api, the old surface
	auth string
}

func setup(t *testing.T) *env {
	t.Helper()
	h := modtest.NewHost("m1")
	mod := groupsmod.New()
	auth, _ := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	api := compat.Handler([]module.Module{mod}, apiserver.New(h, []module.Module{mod}))
	return &env{h: h, api: api, auth: auth}
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

// The old group page names the group in the body for delete, rename and the
// bulk operations; the compat layer has to turn those into module paths.
func TestLegacyGroupCallsReachTheModule(t *testing.T) {
	e := setup(t)

	if out := e.post(t, "/panel/api/clients/groups/create", map[string]any{"name": "friends"}); out["success"] != true {
		t.Fatalf("create: %v", out)
	}
	e.post(t, "/panel/api/clients/groups/bulkAdd", map[string]any{"group": "friends", "emails": []string{"alice", "bob"}})

	// the member list answers a bare array, as the page expects
	emails, ok := e.get(t, "/panel/api/clients/groups/friends/emails").([]any)
	if !ok || len(emails) != 2 {
		t.Fatalf("emails: %#v", emails)
	}

	// the list answers the old shape
	list, ok := e.get(t, "/panel/api/clients/groups").([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("list: %#v", list)
	}
	row := list[0].(map[string]any)
	if row["name"] != "friends" || row["clientCount"] != float64(2) {
		t.Fatalf("row: %v", row)
	}

	// bulkRemove is not tied to one group
	e.post(t, "/panel/api/clients/groups/bulkRemove", map[string]any{"emails": []string{"bob"}})
	emails, _ = e.get(t, "/panel/api/clients/groups/friends/emails").([]any)
	if len(emails) != 1 || emails[0] != "alice" {
		t.Fatalf("after remove: %#v", emails)
	}

	// rename moves the group and keeps its members
	e.post(t, "/panel/api/clients/groups/rename", map[string]any{"oldName": "friends", "newName": "team"})
	emails, _ = e.get(t, "/panel/api/clients/groups/team/emails").([]any)
	if len(emails) != 1 || emails[0] != "alice" {
		t.Fatalf("renamed: %#v", emails)
	}
	if list, _ := e.get(t, "/panel/api/clients/groups").([]any); len(list) != 1 {
		t.Fatalf("old group should be gone: %#v", list)
	}

	// delete takes the name in the body
	if out := e.post(t, "/panel/api/clients/groups/delete", map[string]any{"name": "team"}); out["success"] != true {
		t.Fatalf("delete: %v", out)
	}
	if list, _ := e.get(t, "/panel/api/clients/groups").([]any); len(list) != 0 {
		t.Fatalf("still there: %#v", list)
	}
}

// resetTraffic names the group in the body too, and clears every member's
// counter.
func TestLegacyResetTrafficClearsMembers(t *testing.T) {
	e := setup(t)
	e.post(t, "/panel/api/clients/groups/create", map[string]any{"name": "g"})
	e.post(t, "/panel/api/clients/groups/bulkAdd", map[string]any{"group": "g", "emails": []string{"alice"}})
	ctx := context.Background()
	if _, err := e.h.Set(ctx, usage.Table, "alice", map[string]any{"bytes": int64(1234)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Set(ctx, clients.Table, "alice", map[string]any{"email": "alice", "enable": true}); err != nil {
		t.Fatal(err)
	}
	out := e.post(t, "/panel/api/clients/groups/resetTraffic", map[string]any{"name": "g"})
	if out["success"] != true {
		t.Fatalf("reset: %v", out)
	}
	if rec, ok := e.h.Get(usage.Table, "alice"); !ok {
		t.Fatal("no usage record")
	} else {
		var n int64
		_ = json.Unmarshal(rec["bytes"], &n)
		if n != 0 {
			t.Fatalf("usage = %d", n)
		}
	}
}
