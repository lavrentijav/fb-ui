// Package usage accumulates per-client traffic from node reports and decides
// when a client has used up its allowance. The accumulator is in memory and
// snapshotted to the op log, so a restart resumes from the last snapshot.
package usage

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds the snapshots, keyed by email, field "bytes".
const Table = "usage"

// NodeTable holds per-node snapshots, keyed "email/node", fields "up"/"down" so
// a subscription can report each server's share of the traffic.
const NodeTable = "usage_node"

// CursorTable holds the last report applied per master, field "seq".
const CursorTable = "usage_cursor"

// Tracker is the in-memory accumulator.
type Tracker struct {
	mu    sync.Mutex
	bytes map[string]int64
	nodes map[string]NodeBytes
	dirty bool
}

// NodeBytes is one email's counted traffic on one node.
type NodeBytes struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

func New() *Tracker {
	return &Tracker{bytes: map[string]int64{}, nodes: map[string]NodeBytes{}}
}

// nodeKey is the NodeTable key for an email on a node.
func nodeKey(email, node string) string { return email + "/" + node }

// Load seeds the tracker from the op log snapshots.
func (t *Tracker) Load(r module.Reader) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, email := range r.Keys(Table) {
		rec, ok := r.Get(Table, email)
		if !ok {
			continue
		}
		var n int64
		if json.Unmarshal(rec["bytes"], &n) == nil && n > 0 {
			t.bytes[email] = n
		}
	}
	for _, key := range r.Keys(NodeTable) {
		rec, ok := r.Get(NodeTable, key)
		if !ok {
			continue
		}
		var b NodeBytes
		_ = json.Unmarshal(rec["up"], &b.Up)
		_ = json.Unmarshal(rec["down"], &b.Down)
		if b.Up != 0 || b.Down != 0 {
			t.nodes[key] = b
		}
	}
	t.dirty = false
}

// Add counts one report's traffic deltas, both in the email's total and in the
// node's share (node may be empty when a source does not name it).
func (t *Tracker) Add(email, node string, up, down int64) {
	if email == "" || (up == 0 && down == 0) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bytes[email] += up + down
	if node != "" {
		b := t.nodes[nodeKey(email, node)]
		b.Up += up
		b.Down += down
		t.nodes[nodeKey(email, node)] = b
	}
	t.dirty = true
}

// Node answers one email's traffic on one node.
func (t *Tracker) Node(email, node string) NodeBytes {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nodes[nodeKey(email, node)]
}

// Nodes answers every node's share for one email.
func (t *Tracker) Nodes(email string) map[string]NodeBytes {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]NodeBytes{}
	for key, b := range t.nodes {
		if len(key) > len(email) && key[:len(email)+1] == email+"/" {
			out[key[len(email)+1:]] = b
		}
	}
	return out
}

// Total is the bytes counted for one email.
func (t *Tracker) Total(email string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytes[email]
}

// All is a copy of every total.
func (t *Tracker) All() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int64, len(t.bytes))
	for k, v := range t.bytes {
		out[k] = v
	}
	return out
}

// Snapshot writes every changed total to the op log and clears the dirty flag.
func (t *Tracker) Snapshot(ctx context.Context, h module.Host) error {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]int64, len(t.bytes))
	for k, v := range t.bytes {
		snapshot[k] = v
	}
	nodes := make(map[string]NodeBytes, len(t.nodes))
	for k, v := range t.nodes {
		nodes[k] = v
	}
	t.dirty = false
	t.mu.Unlock()

	emails := make([]string, 0, len(snapshot))
	for email := range snapshot {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	for _, email := range emails {
		if _, err := h.Set(ctx, Table, email, map[string]any{"bytes": snapshot[email]}); err != nil {
			t.mu.Lock()
			t.dirty = true
			t.mu.Unlock()
			return err
		}
	}
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b := nodes[key]
		if _, err := h.Set(ctx, NodeTable, key, map[string]any{"up": b.Up, "down": b.Down}); err != nil {
			t.mu.Lock()
			t.dirty = true
			t.mu.Unlock()
			return err
		}
	}
	return nil
}

// Reset clears one client's total (the panel's "reset traffic").
func (t *Tracker) Reset(ctx context.Context, h module.Host, email string) error {
	t.mu.Lock()
	delete(t.bytes, email)
	for key := range t.nodes {
		if len(key) > len(email) && key[:len(email)+1] == email+"/" {
			delete(t.nodes, key)
		}
	}
	t.mu.Unlock()
	_, err := h.Set(ctx, Table, email, map[string]any{"bytes": int64(0)})
	return err
}
