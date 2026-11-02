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

// CursorTable holds the last report applied per master, field "seq".
const CursorTable = "usage_cursor"

// Tracker is the in-memory accumulator.
type Tracker struct {
	mu    sync.Mutex
	bytes map[string]int64
	dirty bool
}

func New() *Tracker { return &Tracker{bytes: map[string]int64{}} }

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
	t.dirty = false
}

// Add counts one report's traffic deltas.
func (t *Tracker) Add(email string, up, down int64) {
	if email == "" || (up == 0 && down == 0) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bytes[email] += up + down
	t.dirty = true
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
	return nil
}

// Reset clears one client's total (the panel's "reset traffic").
func (t *Tracker) Reset(ctx context.Context, h module.Host, email string) error {
	t.mu.Lock()
	delete(t.bytes, email)
	t.mu.Unlock()
	_, err := h.Set(ctx, Table, email, map[string]any{"bytes": int64(0)})
	return err
}
