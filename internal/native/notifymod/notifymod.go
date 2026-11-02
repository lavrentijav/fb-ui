// Package notifymod turns fleet events into notifications: on the leader it
// reads the event table past a cursor and sends each new event through the
// enabled transports, filtered by each transport's event list.
package notifymod

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/notify"
)

// CursorTable remembers the last event looked at, keyed by transport.
const CursorTable = "notify_cursor"

// StatusTable remembers each transport's latest attempt, keyed by transport.
const StatusTable = "notify_status"

// tickEvery is how often the leader looks for events.
var tickEvery = 2 * time.Second

type Module struct {
	// Config lets tests inject HTTP clients and an SMTP dialer.
	Config notify.Config
}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "notifymod" }
func (*Module) Tables() []string { return []string{CursorTable, StatusTable} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/notify/status", New: "/api/v1/notifymod/status"},
		{Method: "POST", Old: "/panel/api/notify/test/{transport}", New: "/api/v1/notifymod/test/{transport}"},
	}
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/status", module.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]status{}
		for _, name := range h.Keys(StatusTable) {
			rec, ok := h.Get(StatusTable, name)
			if !ok {
				continue
			}
			var s status
			_ = json.Unmarshal(rec["at"], &s.At)
			_ = json.Unmarshal(rec["ok"], &s.OK)
			_ = json.Unmarshal(rec["error"], &s.Error)
			out[name] = s
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	r.Handle("POST", "/test/{transport}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("transport")
		sender := find(notify.Build(h, m.Config), name)
		if sender == nil {
			apiserver.Error(w, http.StatusNotFound, "transport "+name+" is not enabled")
			return
		}
		err := sender.Send(req.Context(), notify.Message{
			Subject: "fbui: test",
			Body:    "This is a test notification from " + h.Name() + ".",
		})
		m.record(req.Context(), h, name, err)
		if err != nil {
			apiserver.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"sent": true})
	})
}

type status struct {
	At    int64  `json:"at"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func find(list []notify.Sender, name string) notify.Sender {
	for _, s := range list {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// Run delivers new events while this master holds the leader lease.
func (m *Module) Run(ctx context.Context, h module.Host) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx, h)
		}
	}
}

type queued struct {
	key  string
	at   int64
	typ  string
	src  string
	data string
}

// tick is one pass over the event table. A transport is only considered when
// it is enabled and wants the event's type; its cursor advances after a
// successful send, so a failed transport retries the same event next pass
// instead of skipping it.
func (m *Module) tick(ctx context.Context, h module.Host) {
	if !h.Leader(ctx) {
		return
	}
	events := m.events(h)
	for _, sender := range notify.Build(h, m.Config) {
		want := notify.EventsFor(h, sender.Name())
		curAt, curKey, armed := m.cursor(h, sender.Name())
		if !armed {
			// a transport seen for the first time starts at the newest event,
			// so enabling it never floods the operator with old history
			at, key := h.Now().UnixMilli(), ""
			if n := len(events); n > 0 && events[n-1].at >= at {
				at, key = events[n-1].at, events[n-1].key
			}
			_, _ = h.Set(ctx, CursorTable, sender.Name(), map[string]any{"afterAt": at, "after": key})
			continue
		}
		lastAt, lastKey, moved := curAt, curKey, false
		for _, e := range events {
			if !after(e, curAt, curKey) {
				continue
			}
			if notify.Wants(want, e.typ) {
				if err := sender.Send(ctx, notify.Render(e.typ, e.src, e.at, e.data)); err != nil {
					m.record(ctx, h, sender.Name(), err)
					break
				}
				m.record(ctx, h, sender.Name(), nil)
			}
			lastAt, lastKey, moved = e.at, e.key, true
		}
		if moved {
			_, _ = h.Set(ctx, CursorTable, sender.Name(), map[string]any{"afterAt": lastAt, "after": lastKey})
		}
	}
}

func (m *Module) events(h module.Host) []queued {
	var out []queued
	for _, key := range h.Keys(module.EventTable) {
		rec, ok := h.Get(module.EventTable, key)
		if !ok {
			continue
		}
		var at int64
		var typ, src string
		_ = json.Unmarshal(rec["at"], &at)
		_ = json.Unmarshal(rec["type"], &typ)
		_ = json.Unmarshal(rec["source"], &src)
		data := ""
		if raw, ok := rec["data"]; ok && len(raw) > 0 && string(raw) != "null" {
			data = string(raw)
		}
		out = append(out, queued{key: key, at: at, typ: typ, src: src, data: data})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].at != out[j].at {
			return out[i].at < out[j].at
		}
		return out[i].key < out[j].key
	})
	return out
}

func after(e queued, at int64, key string) bool {
	return e.at > at || (e.at == at && e.key > key)
}

func (m *Module) cursor(h module.Host, name string) (int64, string, bool) {
	rec, ok := h.Get(CursorTable, name)
	if !ok {
		return 0, "", false
	}
	var at int64
	var key string
	_ = json.Unmarshal(rec["afterAt"], &at)
	_ = json.Unmarshal(rec["after"], &key)
	return at, key, true
}

func (m *Module) record(ctx context.Context, h module.Host, name string, err error) {
	fields := map[string]any{"at": h.Now().UnixMilli(), "ok": err == nil}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	fields["error"] = msg
	_, _ = h.Set(ctx, StatusTable, name, fields)
}
