package accessmod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/accessmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/uimod"
)

type env struct {
	h     *modtest.Host
	api   http.Handler
	admin string
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{h: modtest.NewHost("m1"), admin: ""}
	e.admin, _ = apiauth.Create(context.Background(), e.h, "root", module.ScopeAdmin, 0)
	ui := uimod.New()
	e.api = apiserver.New(e.h, []module.Module{accessmod.New(), ui})
	return e
}

func (e *env) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

func (e *env) create(t *testing.T, body map[string]any) (string, map[string]any) {
	t.Helper()
	rec := e.do("POST", "/api/v1/access/tokens", e.admin, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Token string         `json:"token"`
		View  map[string]any `json:"view"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Token, out.View
}

func TestTokenIsShownOnceAndNeverListed(t *testing.T) {
	e := setup(t)
	token, view := e.create(t, map[string]any{"name": "ci", "scope": "monitor", "ttlHours": 24})
	if token == "" || view["name"] != "ci" || view["scope"] != "monitor" {
		t.Fatalf("create %q %v", token, view)
	}
	if view["live"] != true || view["expires"].(float64) == 0 {
		t.Fatalf("a 24h token should be live and expire: %v", view)
	}
	rec := e.do("GET", "/api/v1/access/tokens", e.admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("the token leaked into the list: %s", rec.Body)
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 || list[0]["name"] != "ci" { // newest first
		t.Fatalf("list %v", list)
	}
}

func TestCreateRefusesBadInput(t *testing.T) {
	e := setup(t)
	if rec := e.do("POST", "/api/v1/access/tokens", e.admin, map[string]any{"name": "", "scope": "admin"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name = %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/access/tokens", e.admin, map[string]any{"name": "x", "scope": "root"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad scope = %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/access/tokens", e.admin, map[string]any{"name": "x", "scope": "admin", "ttlHours": -1}); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative ttl = %d", rec.Code)
	}
}

func TestSessionIsListedAndEndedWithItsToken(t *testing.T) {
	e := setup(t)
	token, view := e.create(t, map[string]any{"name": "alice", "scope": "admin", "ttlHours": 0})
	id := view["id"].(string)

	// the browser logs in with the token and gets a cookie
	rec := e.do("POST", "/api/v1/ui/login", "", map[string]any{"token": token})
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	var sessionID string
	for _, c := range rec.Result().Cookies() {
		if c.Name == uimod.CookieName {
			sessionID = c.Value
		}
	}
	if sessionID == "" {
		t.Fatal("no session cookie")
	}

	rec = e.do("GET", "/api/v1/access/sessions", e.admin, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), sessionID) {
		t.Fatalf("sessions = %d %s", rec.Code, rec.Body)
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["name"] != "alice" || list[0]["token"] != id {
		t.Fatalf("sessions %v", list)
	}

	// the token now shows one session
	tokensRec := e.do("GET", "/api/v1/access/tokens", e.admin, nil)
	var tokens []map[string]any
	_ = json.Unmarshal(tokensRec.Body.Bytes(), &tokens)
	for _, tk := range tokens {
		if tk["id"] == id && tk["sessions"].(float64) != 1 {
			t.Fatalf("token sessions %v", tk)
		}
	}

	// deleting the token ends its session, so the cookie no longer works
	if rec := e.do("DELETE", "/api/v1/access/tokens/"+id, e.admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/access/sessions", e.admin, nil); strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("the session outlived its token: %s", rec.Body)
	}
	req := httptest.NewRequest("GET", "/api/v1/ui/me", nil)
	req.AddCookie(&http.Cookie{Name: uimod.CookieName, Value: sessionID})
	cookieRec := httptest.NewRecorder()
	e.api.ServeHTTP(cookieRec, req)
	if cookieRec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session got %d", cookieRec.Code)
	}
}

func TestSignOutEverywhereEndsOnlyThatTokensSessions(t *testing.T) {
	e := setup(t)
	a, av := e.create(t, map[string]any{"name": "a", "scope": "admin"})
	b, _ := e.create(t, map[string]any{"name": "b", "scope": "admin"})
	for _, tok := range []string{a, b} {
		if rec := e.do("POST", "/api/v1/ui/login", "", map[string]any{"token": tok}); rec.Code != http.StatusOK {
			t.Fatalf("login = %d", rec.Code)
		}
	}
	rec := e.do("POST", "/api/v1/access/tokens/"+av["id"].(string)+"/signout", e.admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("signout = %d %s", rec.Code, rec.Body)
	}
	var out map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["ended"] != 1 {
		t.Fatalf("ended %v", out)
	}
	sessionsRec := e.do("GET", "/api/v1/access/sessions", e.admin, nil)
	var list []map[string]any
	_ = json.Unmarshal(sessionsRec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["name"] != "b" {
		t.Fatalf("sessions %v", list)
	}
}

func TestTheLastAdminTokenCannotBeDeleted(t *testing.T) {
	e := setup(t)
	// the bootstrap token is the only admin one
	var id string
	tokensRec := e.do("GET", "/api/v1/access/tokens", e.admin, nil)
	var list []map[string]any
	_ = json.Unmarshal(tokensRec.Body.Bytes(), &list)
	for _, tk := range list {
		id = tk["id"].(string)
	}
	if rec := e.do("DELETE", "/api/v1/access/tokens/"+id, e.admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("last admin = %d %s", rec.Code, rec.Body)
	}
	// with a second admin it goes
	e.create(t, map[string]any{"name": "second", "scope": "admin"})
	if rec := e.do("DELETE", "/api/v1/access/tokens/"+id, e.admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
}

func TestAnExpiredTokenIsNotLiveAndCannotAuthenticate(t *testing.T) {
	h := modtest.NewHost("m1")
	h.Clock = func() time.Time { return time.Now() }
	tok, err := apiauth.Create(context.Background(), h, "old", module.ScopeMonitor, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h.Clock = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, _, ok := apiauth.Verify(h, tok); ok {
		t.Fatal("an expired token still verifies")
	}
}
