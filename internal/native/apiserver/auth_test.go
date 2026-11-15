package apiserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

type session struct{ name string }

func (s session) Principal(cookie string) (string, module.Scope, bool) {
	if cookie == "good" {
		return s.name, module.ScopeAdmin, true
	}
	return "", "", false
}

func TestRequireAuthClosesTheAdminSurface(t *testing.T) {
	h := modtest.NewHost("m1")
	token, err := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("served")) })
	guard := apiserver.RequireAuth(h, session{"ui"}, func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/public/")
	}, ok)

	cases := []struct {
		name   string
		path   string
		bearer string
		cookie string
		want   int
		body   string
	}{
		{"no credential", "/admin", "", "", http.StatusUnauthorized, "a valid API token is required"},
		{"a wrong token", "/admin", "nope", "", http.StatusUnauthorized, "a valid API token is required"},
		{"the bearer token", "/admin", token, "", http.StatusOK, "served"},
		{"a session cookie", "/admin", "", "good", http.StatusOK, "served"},
		{"a bad cookie", "/admin", "", "bad", http.StatusUnauthorized, "a valid API token is required"},
		{"a public path", "/public/ready", "", "", http.StatusOK, "served"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+c.bearer)
			}
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "fbui_session", Value: c.cookie})
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, r)
			if rec.Code != c.want {
				t.Fatalf("%s = %d, want %d (%s)", c.path, rec.Code, c.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), c.body) {
				t.Fatalf("body %q lacks %q", rec.Body, c.body)
			}
			// the refusal never echoes a credential
			if c.want == http.StatusUnauthorized && c.bearer != "" && strings.Contains(rec.Body.String(), c.bearer) {
				t.Fatalf("the refusal echoed the token: %s", rec.Body)
			}
		})
	}
}
