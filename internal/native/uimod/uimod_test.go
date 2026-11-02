package uimod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/uimod"
)

func setup(t *testing.T) (http.Handler, *modtest.Host, string) {
	t.Helper()
	h := modtest.NewHost("fi1")
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	token, err := apiauth.Create(context.Background(), h, "ops", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	ui := uimod.New()
	api := apiserver.New(h, []module.Module{ui})
	return api, h, token
}

func do(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginSetsAnHttpOnlyCookieAndItAuthenticates(t *testing.T) {
	api, h, token := setup(t)
	if rec := do(t, api, "GET", "/api/v1/ui/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without a session: %d", rec.Code)
	}
	rec := do(t, api, "POST", "/api/v1/ui/login", `{"token":"`+token+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == uimod.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Value == "" {
		t.Fatalf("cookie %+v", cookie)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("the answer echoes the token")
	}

	rec = do(t, api, "GET", "/api/v1/ui/me", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("me with the session: %d %s", rec.Code, rec.Body)
	}
	var me map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me["name"] != "ops" || me["scope"] != "admin" {
		t.Fatalf("me %v", me)
	}
	if len(h.Keys(uimod.Table)) != 1 {
		t.Fatalf("sessions %v", h.Keys(uimod.Table))
	}

	// logout drops the session and the cookie stops working
	rec = do(t, api, "POST", "/api/v1/ui/logout", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := do(t, api, "GET", "/api/v1/ui/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", rec.Code)
	}
	if len(h.Keys(uimod.Table)) != 0 {
		t.Fatalf("sessions left %v", h.Keys(uimod.Table))
	}
}

func TestBadTokenAndRevokedToken(t *testing.T) {
	api, h, token := setup(t)
	if rec := do(t, api, "POST", "/api/v1/ui/login", `{"token":"nope"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a bad token: %d", rec.Code)
	}
	rec := do(t, api, "POST", "/api/v1/ui/login", `{"token":"`+token+`"}`, nil)
	cookie := rec.Result().Cookies()[0]

	// revoking the API token kills the session too
	_, _ = h.Delete(context.Background(), apiauth.Table, apiauth.Hash(token))
	if rec := do(t, api, "GET", "/api/v1/ui/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a session of a revoked token: %d", rec.Code)
	}
}

func TestExpiredSessionIsRefused(t *testing.T) {
	api, h, token := setup(t)
	rec := do(t, api, "POST", "/api/v1/ui/login", `{"token":"`+token+`"}`, nil)
	cookie := rec.Result().Cookies()[0]

	// the clock jumps past the session's lifetime
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0).Add(uimod.TTLDefault + time.Hour) }
	if rec := do(t, api, "GET", "/api/v1/ui/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an expired session: %d", rec.Code)
	}
}
