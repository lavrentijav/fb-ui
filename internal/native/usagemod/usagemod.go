// Package usagemod turns node reports into per-client traffic totals and
// enforces the clients' allowances: the leader counts each report once, keeps
// a snapshot in the op log and disables a client that used up its total.
package usagemod

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// tickEvery is how often the leader counts new reports (default 5 s).
var tickEvery = 5 * time.Second

// maxPerTick caps how many reports one pass reads, so a master returning
// after a long pause does not stall the loop.
const maxPerTick = 2000

type Module struct {
	Source  module.ReportSource
	Masters []string
	Tracker *usage.Tracker
}

// SetReportSource implements module.ReportAware.
func (m *Module) SetReportSource(src module.ReportSource, masters []string) {
	m.Source, m.Masters = src, masters
}

func New() *Module { return &Module{Tracker: usage.New()} }

func (*Module) Name() string { return "usage" }
func (*Module) Tables() []string {
	return []string{usage.Table, usage.NodeTable, usage.CursorTable}
}

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/usage/list", New: "/api/v1/usage"},
		{Method: "POST", Old: "/panel/api/usage/reset/{email}", New: "/api/v1/usage/{email}/reset"},
	}
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, m.Tracker.All())
	})
	r.Handle("GET", "/{email}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		apiserver.JSON(w, http.StatusOK, map[string]any{
			"email": req.PathValue("email"), "bytes": m.Tracker.Total(req.PathValue("email")),
		})
	})
	r.Handle("POST", "/{email}/reset", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		email := req.PathValue("email")
		if err := m.Tracker.Reset(req.Context(), h, email); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"email": email, "bytes": 0})
	})
}

// Run counts reports and enforces allowances while this master leads.
func (m *Module) Run(ctx context.Context, h module.Host) {
	m.Tracker.Load(h)
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !h.Leader(ctx) {
				continue
			}
			if err := m.tick(ctx, h); err != nil {
				continue
			}
		}
	}
}

// tick counts new reports, snapshots and enforces the allowances, in that
// order, so a client disabled in this pass is already counted to the byte.
func (m *Module) tick(ctx context.Context, h module.Host) error {
	if m.Source == nil {
		return nil
	}
	for _, master := range m.Masters {
		first, last, err := m.Source.ReportRange(ctx, master)
		if err != nil || last == 0 {
			continue
		}
		var pos uint64
		if rec, ok := h.Get(usage.CursorTable, master); ok {
			var n int64
			_ = json.Unmarshal(rec["seq"], &n)
			pos = uint64(n)
		}
		if first > 0 && pos+1 < first {
			pos = first - 1 // the gap aged out; counting resumes from what is left
		}
		read := 0
		for seq := pos + 1; seq <= last && read < maxPerTick; seq++ {
			raw, err := m.Source.GetReport(ctx, master, seq)
			if err != nil {
				break
			}
			m.apply(raw)
			pos, read = seq, read+1
		}
		if _, err := h.Set(ctx, usage.CursorTable, master, map[string]any{"seq": int64(pos)}); err != nil {
			return err
		}
	}
	if err := m.Tracker.Snapshot(ctx, h); err != nil {
		return err
	}
	m.enforce(ctx, h)
	return nil
}

// apply adds one report's traffic to the totals; a malformed report is
// skipped, its stream position still advances with the cursor.
func (m *Module) apply(raw []byte) {
	var r struct {
		Node    string `json:"n"`
		Traffic []struct {
			Email string `json:"e"`
			Up    int64  `json:"u"`
			Down  int64  `json:"d"`
		} `json:"t"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	for _, t := range r.Traffic {
		m.Tracker.Add(t.Email, r.Node, t.Up, t.Down)
	}
}

// enforce disables every enabled client that reached its allowance and tells
// the fleet about it. A disabled record is left alone, so the sweep is
// idempotent and only writes a change once.
func (m *Module) enforce(ctx context.Context, h module.Host) {
	for _, c := range clients.List(h) {
		if !c.Enable || c.TotalGB <= 0 {
			continue
		}
		if m.Tracker.Total(c.Email) < c.TotalGB {
			continue
		}
		c.Enable = false
		if _, _, err := clients.Save(ctx, h, c); err != nil {
			continue
		}
		_ = h.Emit(ctx, module.Event{
			Type: "client.exhausted", Source: c.Node, At: h.Now().UnixMilli(),
			Data: json.RawMessage(`{"email":` + quote(c.Email) + `,"node":` + quote(c.Node) +
				`,"inbound":` + quote(c.Inbound) + `,"bytes":` + itoa(m.Tracker.Total(c.Email)) + `}`),
		})
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
