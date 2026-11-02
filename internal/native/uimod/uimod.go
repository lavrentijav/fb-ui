// Package uimod is the interface's server side: a browser logs in once with
// an API token and gets an HttpOnly session cookie, so the page keeps no
// token in JavaScript. The page itself is served by the master from a
// directory (Config.UIDir), not embedded here.
package uimod

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table keeps live sessions; keys are SHA-256 of the cookie value.
const Table = "session"

// CookieName is the session cookie the browser carries.
const CookieName = "fbui_session"

// TTLDefault bounds a session; the page renews it by logging in again.
const TTLDefault = 12 * time.Hour

type Module struct {
	TTL time.Duration

	host module.Host // set by Mount, before the server starts
}

func New() *Module { return &Module{TTL: TTLDefault} }

func (m *Module) Name() string     { return "ui" }
func (m *Module) Tables() []string { return []string{Table} }

func (m *Module) Mount(r module.Router, h module.Host) {
	m.host = h
	// public: a browser has no token yet when it logs in
	r.Handle("POST", "/login", module.ScopePublic, func(w http.ResponseWriter, req *http.Request) {
		var body struct{ Token string }
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, "a JSON body {\"token\": \"...\"} is required")
			return
		}
		scope, name, ok := apiauth.Verify(h, strings.TrimSpace(body.Token))
		if !ok {
			apiserver.Error(w, http.StatusUnauthorized, "a valid API token is required")
			return
		}
		id, err := sessionID()
		if err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		expires := h.Now().Add(m.ttl())
		if _, err := h.Set(req.Context(), Table, hashID(id), map[string]any{
			"token":   apiauth.Hash(strings.TrimSpace(body.Token)),
			"name":    name,
			"scope":   scope,
			"expires": expires.UnixMilli(),
		}); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: req.TLS != nil, Expires: expires,
		})
		apiserver.JSON(w, http.StatusOK, map[string]any{"name": name, "scope": scope, "expires": expires.UnixMilli()})
	})

	r.Handle("POST", "/logout", module.ScopePublic, func(w http.ResponseWriter, req *http.Request) {
		if c, err := req.Cookie(CookieName); err == nil && c.Value != "" {
			_, _ = h.Delete(req.Context(), Table, hashID(c.Value))
		}
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			MaxAge: -1,
		})
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	r.Handle("GET", "/me", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		name, scope := apiserver.Principal(req)
		apiserver.JSON(w, http.StatusOK, map[string]any{"name": name, "scope": scope})
	})
}

// Principal implements module.SessionAuth: a cookie names a live session
// whose API token must still exist and be live.
func (m *Module) Principal(cookie string) (string, module.Scope, bool) {
	h := m.host
	if h == nil || cookie == "" {
		return "", "", false
	}
	rec, ok := h.Get(Table, hashID(cookie))
	if !ok {
		return "", "", false
	}
	var expires int64
	var name string
	var scope module.Scope
	var tokenHash string
	_ = json.Unmarshal(rec["expires"], &expires)
	_ = json.Unmarshal(rec["name"], &name)
	_ = json.Unmarshal(rec["scope"], &scope)
	_ = json.Unmarshal(rec["token"], &tokenHash)
	if expires > 0 && h.Now().UnixMilli() >= expires {
		_, _ = h.Delete(context.Background(), Table, hashID(cookie))
		return "", "", false
	}
	if _, _, ok := apiauth.ByHash(h, tokenHash); !ok {
		return "", "", false
	}
	return name, scope, true
}

func (m *Module) Legacy() []module.LegacyRoute { return nil }

// Run drops expired sessions once an hour on the leader.
func (m *Module) Run(ctx context.Context, h module.Host) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !h.Leader(ctx) {
				continue
			}
			for _, key := range h.Keys(Table) {
				rec, ok := h.Get(Table, key)
				if !ok {
					continue
				}
				var expires int64
				_ = json.Unmarshal(rec["expires"], &expires)
				if expires > 0 && h.Now().UnixMilli() >= expires {
					_, _ = h.Delete(ctx, Table, key)
				}
			}
		}
	}
}

func (m *Module) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return TTLDefault
}

func sessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
