// Package accessmod is the operator's view of who may use the API: the tokens
// that authenticate it and the browser sessions minted from them. Tokens are
// stored as hashes in the op log, so this module can never show one again --
// only its name, scope and expiry.
package accessmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/uimod"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "access" }
func (*Module) Tables() []string { return []string{apiauth.Table, uimod.Table} }

func (*Module) Legacy() []module.LegacyRoute { return nil }

// tokenView is a token as the operator sees it: never the token itself.
type tokenView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Expires  int64  `json:"expires"`
	At       int64  `json:"at"`
	Live     bool   `json:"live"`
	Sessions int    `json:"sessions"`
}

// sessionView is one browser session: who holds it and until when.
type sessionView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	Expires int64  `json:"expires"`
	At      int64  `json:"at"`
	Token   string `json:"token"`
}

func str(rec map[string]json.RawMessage, k string) (v string) { _ = json.Unmarshal(rec[k], &v); return }
func num(rec map[string]json.RawMessage, k string) (v int64)  { _ = json.Unmarshal(rec[k], &v); return }

func tokens(h module.Host, now time.Time) []tokenView {
	out := make([]tokenView, 0, len(h.Keys(apiauth.Table)))
	for _, id := range h.Keys(apiauth.Table) {
		rec, ok := h.Get(apiauth.Table, id)
		if !ok {
			continue
		}
		expires := num(rec, "expires")
		v := tokenView{
			ID:      id,
			Name:    str(rec, "name"),
			Scope:   str(rec, "scope"),
			Expires: expires,
			At:      num(rec, "at"),
			Live:    expires == 0 || now.UnixMilli() < expires,
		}
		for _, key := range h.Keys(uimod.Table) {
			srec, ok := h.Get(uimod.Table, key)
			if ok && str(srec, "token") == id {
				v.Sessions++
			}
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b tokenView) int {
		if a.At != b.At {
			return int(b.At - a.At) // newest first
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func sessions(h module.Host) []sessionView {
	out := make([]sessionView, 0, len(h.Keys(uimod.Table)))
	for _, id := range h.Keys(uimod.Table) {
		rec, ok := h.Get(uimod.Table, id)
		if !ok {
			continue
		}
		out = append(out, sessionView{
			ID:      id,
			Name:    str(rec, "name"),
			Scope:   str(rec, "scope"),
			Expires: num(rec, "expires"),
			At:      num(rec, "at"),
			Token:   str(rec, "token"),
		})
	}
	slices.SortFunc(out, func(a, b sessionView) int {
		if a.At != b.At {
			return int(b.At - a.At)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// lastAdmin says whether id is the only live admin token left; deleting it
// would lock every operator out of the API.
func lastAdmin(h module.Host, id string, now time.Time) bool {
	admins := 0
	for _, t := range tokens(h, now) {
		if t.Live && t.Scope == string(module.ScopeAdmin) {
			admins++
			if t.ID == id {
				admins--
			}
		}
	}
	return admins == 0
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/tokens", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, tokens(h, h.Now()))
	})
	r.Handle("POST", "/tokens", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Name     string `json:"name"`
			Scope    string `json:"scope"`
			TTLHours int    `json:"ttlHours"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, "a JSON body is required")
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			apiserver.Error(w, http.StatusBadRequest, "a name is required")
			return
		}
		scope := module.Scope(body.Scope)
		if scope != module.ScopeAdmin && scope != module.ScopeMonitor {
			apiserver.Error(w, http.StatusBadRequest, `scope must be "admin" or "monitor"`)
			return
		}
		if body.TTLHours < 0 {
			apiserver.Error(w, http.StatusBadRequest, "ttlHours cannot be negative")
			return
		}
		token, err := apiauth.Create(req.Context(), h, body.Name, scope, time.Duration(body.TTLHours)*time.Hour)
		if err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		id := apiauth.Hash(token)
		var view tokenView
		for _, t := range tokens(h, h.Now()) {
			if t.ID == id {
				view = t
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "view": view})
	})
	r.Handle("DELETE", "/tokens/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(apiauth.Table, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such token")
			return
		}
		if lastAdmin(h, id, h.Now()) {
			apiserver.Error(w, http.StatusConflict, "this is the last admin token; create another one first")
			return
		}
		if err := m.revoke(req.Context(), h, id); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	r.Handle("POST", "/tokens/{id}/signout", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(apiauth.Table, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such token")
			return
		}
		n := 0
		for _, s := range sessions(h) {
			if s.Token == id {
				if err := m.dropSession(req.Context(), h, s.ID); err == nil {
					n++
				}
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]int{"ended": n})
	})
	r.Handle("GET", "/sessions", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, sessions(h))
	})
	r.Handle("DELETE", "/sessions/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(uimod.Table, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such session")
			return
		}
		if err := m.dropSession(req.Context(), h, id); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
}

// revoke deletes a token and every session minted from it: a session is only
// valid while its token is, so both go together.
func (m *Module) revoke(ctx context.Context, h module.Host, id string) error {
	for _, s := range sessions(h) {
		if s.Token == id {
			if err := m.dropSession(ctx, h, s.ID); err != nil {
				return err
			}
		}
	}
	_, err := h.Delete(ctx, apiauth.Table, id)
	return err
}

func (m *Module) dropSession(ctx context.Context, h module.Host, id string) error {
	if id == "" {
		return errors.New("an empty session id")
	}
	_, err := h.Delete(ctx, uimod.Table, id)
	return err
}

// Run keeps nothing alive: sessions and tokens expire on their own and are
// pruned by their owning modules.
func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }
