// Package presence tells the fleet who is online where: every node
// publishes its online users, every node and master keeps the newest.
package presence

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

func Subject(node string) string { return "presence." + node }

type Report struct {
	Node  string   `json:"n"`
	Users []string `json:"u"`
	At    int64    `json:"at"` // unix ms
}

func Publish(nc *nats.Conn, node string, users []string, at time.Time) error {
	data, err := json.Marshal(Report{Node: node, Users: users, At: at.UnixMilli()})
	if err != nil {
		return err
	}
	return nc.Publish(Subject(node), data)
}

type seen struct {
	node string
	at   int64
}

// Map is the fleet's presence; a node silent for TTL is forgotten.
type Map struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	reports map[string]Report // by node
	last    map[string]string
	changed chan struct{}
}

func (m *Map) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Map) init() {
	if m.reports == nil {
		m.reports = map[string]Report{}
		m.changed = make(chan struct{}, 1)
	}
}

func (m *Map) Add(r Report) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if old, ok := m.reports[r.Node]; ok && old.At > r.At {
		return
	}
	m.reports[r.Node] = r
	if w := m.whereLocked(); !sameMap(w, m.last) {
		m.last = w
		select {
		case m.changed <- struct{}{}:
		default:
		}
	}
}

// Where maps each online user to the node with its newest report.
func (m *Map) Where() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.whereLocked()
}

func (m *Map) whereLocked() map[string]string {
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	cutoff := m.now().Add(-ttl).UnixMilli()
	best := map[string]seen{}
	for node, r := range m.reports {
		if r.At < cutoff {
			continue
		}
		for _, u := range r.Users {
			if b, ok := best[u]; !ok || r.At > b.at {
				best[u] = seen{node, r.At}
			}
		}
	}
	out := make(map[string]string, len(best))
	for u, s := range best {
		out[u] = s.node
	}
	return out
}

func (m *Map) Changed() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.changed
}

// Watch feeds the map from every node's reports.
func (m *Map) Watch(nc *nats.Conn) (*nats.Subscription, error) {
	return nc.Subscribe(Subject("*"), func(msg *nats.Msg) {
		var r Report
		if json.Unmarshal(msg.Data, &r) == nil && r.Node != "" && Subject(r.Node) == msg.Subject {
			m.Add(r)
		}
	})
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
