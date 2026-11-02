// Package nodestate turns the newest node reports into what the interface
// shows: who is online, when a node last spoke and whether it drains.
package nodestate

import (
	"sort"
	"time"
)

// Report is the part of a node report this package needs.
type Report struct {
	Node   string
	At     int64 // unix ms
	Online []string
	Drain  *Drain
	Health *Health
}

// Health is the node's vitals from its report.
type Health struct {
	Load1    float64 `json:"load1,omitempty"`
	MemUsed  int64   `json:"memUsed,omitempty"`
	MemTotal int64   `json:"memTotal,omitempty"`
	DiskFree int64   `json:"diskFree,omitempty"`
	Uptime   int64   `json:"uptime,omitempty"`
	Version  string  `json:"version,omitempty"`
}

// Drain is a node in maintenance.
type Drain struct {
	Since  int64
	Active int
}

// State is one node as the fleet sees it.
type State struct {
	Node        string   `json:"node"`
	HasSpec     bool     `json:"hasSpec"`
	LastSeen    int64    `json:"lastSeen"` // unix ms, 0 = never
	Online      bool     `json:"online"`
	OnlineUsers []string `json:"onlineUsers,omitempty"`
	Draining    bool     `json:"draining,omitempty"`
	DrainSince  int64    `json:"drainSince,omitempty"`
	DrainActive int      `json:"drainActive,omitempty"`
	Health      *Health  `json:"health,omitempty"`
}

// DefaultStale is how long after its last report a node stops counting as
// online; agents report every 10 s by default.
const DefaultStale = 60 * time.Second

// Aggregate merges the newest report per node with the nodes that only have a
// spec. nodes is the set of registered names; reports must be the newest one
// per node (newer wins if not).
func Aggregate(nodes []string, reports []Report, now time.Time, stale time.Duration) []State {
	if stale <= 0 {
		stale = DefaultStale
	}
	byNode := map[string]Report{}
	for _, r := range reports {
		if r.Node == "" {
			continue
		}
		if old, ok := byNode[r.Node]; !ok || r.At > old.At {
			byNode[r.Node] = r
		}
	}
	seen := map[string]bool{}
	var out []State
	for _, n := range nodes {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		s := State{Node: n, HasSpec: true}
		if r, ok := byNode[n]; ok {
			s = state(n, r, now, stale)
			s.HasSpec = true
		}
		out = append(out, s)
	}
	for n, r := range byNode {
		if seen[n] {
			continue
		}
		out = append(out, state(n, r, now, stale))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

func state(node string, r Report, now time.Time, stale time.Duration) State {
	s := State{Node: node, LastSeen: r.At, Online: r.At > 0 && now.UnixMilli()-r.At <= stale.Milliseconds()}
	if s.Online {
		s.OnlineUsers = append([]string(nil), r.Online...)
		sort.Strings(s.OnlineUsers)
	}
	if r.Drain != nil {
		s.Draining = true
		s.DrainSince = r.Drain.Since
		s.DrainActive = r.Drain.Active
	}
	if r.Health != nil {
		h := *r.Health
		s.Health = &h
	}
	return s
}
