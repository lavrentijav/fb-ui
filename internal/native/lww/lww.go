// Package lww merges ops field by field: the newest stamp wins, a delete hides
// older fields; any delivery order gives the same state.
package lww

import (
	"crypto/sha256"
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

type field struct {
	stamp hlc.Stamp
	value json.RawMessage
}

type record struct {
	fields  map[string]field
	deleted hlc.Stamp
}

// visible drops fields older than the tombstone; a field written after a
// delete brings the record back with only the newer fields.
func (r *record) visible() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, f := range r.fields {
		if f.stamp.After(r.deleted) {
			out[name] = f.value
		}
	}
	return out
}

// State is safe for concurrent use.
type State struct {
	mu     sync.RWMutex
	tables map[string]map[string]*record
}

func New() *State { return &State{tables: map[string]map[string]*record{}} }

// Apply merges one op. It reports whether anything visible could have
// changed, so callers only re-render what an op touched.
func (s *State) Apply(op oplog.Op) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tbl := s.tables[op.Table]
	if tbl == nil {
		tbl = map[string]*record{}
		s.tables[op.Table] = tbl
	}
	rec := tbl[op.Key]
	if rec == nil {
		rec = &record{fields: map[string]field{}}
		tbl[op.Key] = rec
	}
	changed := false
	if op.Delete && op.Stamp.After(rec.deleted) {
		rec.deleted, changed = op.Stamp, true
	}
	for name, v := range op.Fields {
		if cur, ok := rec.fields[name]; !ok || op.Stamp.After(cur.stamp) {
			rec.fields[name] = field{stamp: op.Stamp, value: slices.Clone(v)}
			changed = true
		}
	}
	return changed
}

// Get returns the visible fields of a record, false when it does not exist
// or is deleted.
func (s *State) Get(table, key string) (map[string]json.RawMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.tables[table][key]
	if rec == nil {
		return nil, false
	}
	f := rec.visible()
	return f, len(f) > 0
}

// Keys lists the live records of a table, sorted.
func (s *State) Keys(table string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for key, rec := range s.tables[table] {
		if len(rec.visible()) > 0 {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// Digest hashes the visible state; two masters with the same digest agree.
func (s *State) Digest() [32]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := sha256.New()
	for _, table := range slices.Sorted(maps.Keys(s.tables)) {
		for _, key := range slices.Sorted(maps.Keys(s.tables[table])) {
			vis := s.tables[table][key].visible()
			if len(vis) == 0 {
				continue
			}
			h.Write([]byte(table + "\x00" + key + "\x00"))
			for _, name := range slices.Sorted(maps.Keys(vis)) {
				h.Write([]byte(name + "\x00"))
				h.Write(vis[name])
				h.Write([]byte{0})
			}
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
