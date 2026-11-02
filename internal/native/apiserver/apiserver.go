// Package apiserver serves /api/v1: every registered module mounted under its
// name, behind Bearer tokens, with an OpenAPI document of what is mounted.
package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type principalKey struct{}

type principal struct {
	name  string
	scope module.Scope
}

// Principal is who the request's token belongs to.
func Principal(r *http.Request) (name string, scope module.Scope) {
	if p, ok := r.Context().Value(principalKey{}).(principal); ok {
		return p.name, p.scope
	}
	return "", ""
}

// JSON writes v as the response body.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg}.
func Error(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, map[string]string{"error": msg})
}

type route struct {
	method, path string
	scope        module.Scope
}

type router struct {
	mux     *http.ServeMux
	host    module.Host
	session module.SessionAuth
	prefix  string
	wrap    []func(http.Handler) http.Handler
	routes  *[]route
}

func (r *router) Handle(method, pattern string, scope module.Scope, h http.HandlerFunc) {
	path := r.prefix
	if pattern != "/" {
		path += pattern
	}
	*r.routes = append(*r.routes, route{method, path, scope})
	var inner http.Handler = h
	if scope != module.ScopePublic {
		inner = authorize(scope, inner)
	}
	for i := len(r.wrap) - 1; i >= 0; i-- {
		inner = r.wrap[i](inner)
	}
	if scope == module.ScopePublic {
		r.mux.Handle(method+" "+path, inner)
		return
	}
	r.mux.Handle(method+" "+path, authenticate(r.host, r.session, inner))
}

// authenticate puts the token's owner in the context; a missing or unknown
// token never reaches the module. A module implementing module.SessionAuth
// may also identify the caller by cookie (the interface does that).
func authenticate(h module.Host, session module.SessionAuth, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		scope, name, ok := apiauth.Verify(h, strings.TrimSpace(bearer))
		if !ok && session != nil {
			if c, err := r.Cookie("fbui_session"); err == nil && c.Value != "" {
				name, scope, ok = session.Principal(c.Value)
			}
		}
		if !ok {
			Error(w, http.StatusUnauthorized, "a valid API token is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{name, scope})))
	})
}

// RequireAuth guards an administrative handler: a request the public predicate
// accepts passes unchecked, everything else needs a bearer token or (when the
// fleet has a session module) its login cookie. It is how the master's admin
// surface stays closed while the subscription, enrollment and panel paths keep
// their own credentials.
func RequireAuth(h module.Host, session module.SessionAuth, public func(*http.Request) bool, next http.Handler) http.Handler {
	guarded := authenticate(h, session, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public != nil && public(r) {
			next.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// authorize checks the scope after the module middlewares ran, so a refused
// change is still seen by them (the audit log records it).
func authorize(need module.Scope, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, have := Principal(r); !apiauth.Allows(have, need) {
			Error(w, http.StatusForbidden, "this token may not do that")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// New builds the handler for /api/v1.
func New(h module.Host, mods []module.Module) http.Handler {
	mux := http.NewServeMux()
	var routes []route
	var wrap []func(http.Handler) http.Handler
	var session module.SessionAuth
	for _, m := range mods {
		if mw, ok := m.(module.Middlewarer); ok {
			wrap = append(wrap, mw.Middleware(h))
		}
		if sa, ok := m.(module.SessionAuth); ok {
			session = sa
		}
	}
	for _, m := range mods {
		m.Mount(&router{mux: mux, host: h, session: session, prefix: "/api/v1/" + m.Name(), wrap: wrap, routes: &routes}, h)
	}
	mux.HandleFunc("GET /api/v1/ready", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /api/v1/openapi.json", authenticate(h, session, authorize(module.ScopeAdmin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, openAPI(routes))
	}))))
	return mux
}

func openAPI(routes []route) map[string]any {
	sorted := append([]route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].path != sorted[j].path {
			return sorted[i].path < sorted[j].path
		}
		return sorted[i].method < sorted[j].method
	})
	paths := map[string]map[string]any{}
	for _, rt := range sorted {
		if paths[rt.path] == nil {
			paths[rt.path] = map[string]any{}
		}
		paths[rt.path][strings.ToLower(rt.method)] = map[string]any{
			"x-scope":   string(rt.scope),
			"responses": map[string]any{"200": map[string]any{"description": "OK"}},
		}
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]string{"title": "fbui master API", "version": "1"},
		"paths":   paths,
	}
}
