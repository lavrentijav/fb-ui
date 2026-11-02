// Package dashboardmod answers the overview page in one call: how many nodes
// are up, how many people are online, and how much traffic was counted.
package dashboardmod

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestate"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// SpecTable is where node specs live; a node without one is still shown.
const SpecTable = "nodespec"

type Module struct {
	src     module.ReportSource
	masters []string
}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "dashboard" }
func (*Module) Tables() []string { return nil } // it only reads

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{{Method: "GET", Old: "/panel/api/dashboard/summary", New: "/api/v1/dashboard"}}
}

// SetReportSource implements module.ReportAware.
func (m *Module) SetReportSource(src module.ReportSource, masters []string) {
	m.src, m.masters = src, masters
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

// Summary is the overview's numbers.
type Summary struct {
	Nodes   Nodes            `json:"nodes"`
	Clients Clients          `json:"clients"`
	Traffic Traffic          `json:"traffic"`
	Usage   map[string]int64 `json:"usage,omitempty"`
}

type Nodes struct {
	Total    int `json:"total"`
	Online   int `json:"online"`
	Draining int `json:"draining"`
	Users    int `json:"usersOnline"`
}

type Clients struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
	Expired  int `json:"expired"`
}

type Traffic struct {
	Bytes int64 `json:"bytes"`
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		apiserver.JSON(w, http.StatusOK, m.summary(req.Context(), h))
	})
	// just one number, for a status widget or a bot
	r.Handle("GET", "/online", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		s := m.summary(req.Context(), h)
		apiserver.JSON(w, http.StatusOK, s.Nodes)
	})
}

// Summary lets the master's admin API answer the overview without a token.
func (m *Module) Summary(ctx context.Context, h module.Host) any { return m.summary(ctx, h) }

func (m *Module) summary(ctx context.Context, h module.Host) Summary {
	var s Summary
	var reps []nodestate.Report
	if m.src != nil {
		reps = nodestate.Collect(ctx, m.src, m.masters, 2000)
	}
	states := nodestate.Aggregate(h.Keys(SpecTable), reps, h.Now(), nodestate.DefaultStale)
	s.Nodes.Total = len(states)
	for _, st := range states {
		if st.Online {
			s.Nodes.Online++
			s.Nodes.Users += len(st.OnlineUsers)
		}
		if st.Draining {
			s.Nodes.Draining++
		}
	}
	now := h.Now().UnixMilli()
	for _, c := range clients.List(h) {
		s.Clients.Total++
		if c.Enable {
			s.Clients.Enabled++
		} else {
			s.Clients.Disabled++
		}
		if c.ExpiresAt > 0 && now >= c.ExpiresAt {
			s.Clients.Expired++
		}
	}
	for _, email := range h.Keys(usage.Table) {
		rec, ok := h.Get(usage.Table, email)
		if !ok {
			continue
		}
		var n int64
		_ = json.Unmarshal(rec["bytes"], &n)
		s.Traffic.Bytes += n
	}
	return s
}
