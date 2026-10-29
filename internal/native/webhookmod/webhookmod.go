// Package webhookmod is webhooks as a master module: endpoints that receive
// the panel's events as signed POSTs. Configuration replicates through the op
// log; delivery is done by the leader only (see deliver.go).
package webhookmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhook"
)

// Op log tables.
const (
	Table       = "webhook"        // key = id; name, url, events, enable, secret, createdAt
	StatusTable = "webhook_status" // key = id; at, ok, error
	CursorTable = "webhook_cursor" // key = id; after = key of the last delivered event
)

var checkURL = webhook.CheckPublicURL

type Module struct {
	// Sender delivers; tests tune its retry waits.
	Sender *webhook.Sender
}

func New() *Module { return &Module{Sender: webhook.New(nil)} }

func (*Module) Name() string     { return "webhooks" }
func (*Module) Tables() []string { return []string{Table, StatusTable, CursorTable} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/webhooks/list", New: "/api/v1/webhooks"},
		{Method: "POST", Old: "/panel/api/webhooks/add", New: "/api/v1/webhooks"},
		{Method: "POST", Old: "/panel/api/webhooks/update/{id}", New: "/api/v1/webhooks/{id}", NewMethod: "PUT"},
		{Method: "POST", Old: "/panel/api/webhooks/del/{id}", New: "/api/v1/webhooks/{id}", NewMethod: "DELETE"},
		{Method: "POST", Old: "/panel/api/webhooks/test/{id}", New: "/api/v1/webhooks/{id}/test"},
	}
}

// Matches says whether a webhook listening for events wants one of type typ;
// an empty list means every event.
func Matches(events []string, typ string) bool {
	return len(events) == 0 || slices.Contains(events, typ)
}

type input struct {
	Name   string   `json:"name"`
	Url    string   `json:"url"`
	Events []string `json:"events"`
	Enable *bool    `json:"enable"`
	Secret string   `json:"secret"`
}

func (in *input) clean() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("a name is required")
	}
	in.Url = strings.TrimSpace(in.Url)
	if err := checkURL(in.Url); err != nil {
		return errors.New("address: " + err.Error())
	}
	events := make([]string, 0, len(in.Events))
	for _, e := range in.Events {
		if e = strings.TrimSpace(e); e != "" && !slices.Contains(events, e) {
			events = append(events, e)
		}
	}
	in.Events = events
	return nil
}

func decode(w http.ResponseWriter, r *http.Request) (*input, bool) {
	var in input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		apiserver.Error(w, http.StatusBadRequest, "unreadable body")
		return nil, false
	}
	if err := in.clean(); err != nil {
		apiserver.Error(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &in, true
}

func str(rec map[string]json.RawMessage, k string) (v string) { _ = json.Unmarshal(rec[k], &v); return }
func num(rec map[string]json.RawMessage, k string) (v int64)  { _ = json.Unmarshal(rec[k], &v); return }
func flag(rec map[string]json.RawMessage, k string) (v bool)  { _ = json.Unmarshal(rec[k], &v); return }

// view is what the API shows of a webhook: never the secret.
func view(h module.Host, id string) (map[string]any, bool) {
	rec, ok := h.Get(Table, id)
	if !ok {
		return nil, false
	}
	var events []string
	_ = json.Unmarshal(rec["events"], &events)
	out := map[string]any{
		"id": id, "name": str(rec, "name"), "url": str(rec, "url"), "events": events,
		"enable": flag(rec, "enable"), "hasSecret": str(rec, "secret") != "", "createdAt": num(rec, "createdAt"),
		"lastAt": int64(0), "lastOk": false, "lastError": "",
	}
	if st, ok := h.Get(StatusTable, id); ok {
		out["lastAt"], out["lastOk"], out["lastError"] = num(st, "at"), flag(st, "ok"), str(st, "error")
	}
	return out, true
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		out := []map[string]any{}
		for _, id := range h.Keys(Table) {
			if v, ok := view(h, id); ok {
				out = append(out, v)
			}
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	r.Handle("POST", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		in, ok := decode(w, req)
		if !ok {
			return
		}
		id := h.NextKey()
		enable := in.Enable == nil || *in.Enable
		if _, err := h.Set(req.Context(), Table, id, map[string]any{
			"name": in.Name, "url": in.Url, "events": in.Events, "enable": enable,
			"secret": in.Secret, "createdAt": h.Now().UnixMilli(),
		}); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		v, _ := view(h, id)
		apiserver.JSON(w, http.StatusOK, v)
	})
	r.Handle("PUT", "/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		old, exists := h.Get(Table, id)
		if !exists {
			apiserver.Error(w, http.StatusNotFound, "no such webhook")
			return
		}
		in, ok := decode(w, req)
		if !ok {
			return
		}
		fields := map[string]any{"name": in.Name, "url": in.Url, "events": in.Events, "enable": in.Enable == nil || *in.Enable}
		if in.Secret != "" {
			fields["secret"] = in.Secret // an empty one keeps the stored secret
		} else {
			fields["secret"] = str(old, "secret")
		}
		if _, err := h.Set(req.Context(), Table, id, fields); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		v, _ := view(h, id)
		apiserver.JSON(w, http.StatusOK, v)
	})
	r.Handle("DELETE", "/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(Table, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such webhook")
			return
		}
		for _, t := range []string{Table, StatusTable, CursorTable} {
			if _, err := h.Delete(req.Context(), t, id); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	r.Handle("POST", "/{id}/test", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(Table, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such webhook")
			return
		}
		ev := webhook.Event{Type: "webhook.test", Source: "fbui", Time: h.Now()}
		if err := m.Deliver(req.Context(), h, id, ev, h.NextKey()); err != nil {
			apiserver.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "delivered"})
	})
}

// Deliver sends one event to one webhook and records the outcome.
func (m *Module) Deliver(ctx context.Context, h module.Host, id string, ev webhook.Event, delivery string) error {
	rec, ok := h.Get(Table, id)
	if !ok {
		return errors.New("no such webhook")
	}
	url := str(rec, "url")
	err := checkURL(url) // checked again: DNS may have moved since it was saved
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err = m.Sender.Send(sctx, webhook.Target{URL: url, Secret: str(rec, "secret"), Delivery: delivery}, ev)
		cancel()
	}
	status := map[string]any{"at": h.Now().Unix(), "ok": err == nil, "error": ""}
	if err != nil {
		status["error"] = err.Error()
	}
	_, _ = h.Set(ctx, StatusTable, id, status)
	return err
}

// Run is filled in by deliver.go.
