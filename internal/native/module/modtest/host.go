// Package modtest is a single-master Host over the in-memory projection, for
// testing modules without a bus.
package modtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lww"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Host implements module.Host for one master named at construction.
type Host struct {
	// IsLeader is what Leader returns.
	IsLeader bool
	// Clock replaces the wall clock when set.
	Clock func() time.Time

	name  string
	state *lww.State
	clock *hlc.Clock
	mu    sync.Mutex
	seq   uint64
	keys  uint64
}

func NewHost(name string) *Host {
	h := &Host{name: name, state: lww.New()}
	h.clock = hlc.NewWithTime(name, func() int64 { return h.Now().UnixMilli() })
	return h
}

func (h *Host) Name() string { return h.name }

func (h *Host) Now() time.Time {
	if h.Clock != nil {
		return h.Clock()
	}
	return time.Now()
}

func (h *Host) Leader(context.Context) bool { return h.IsLeader }

func (h *Host) write(op oplog.Op) oplog.Op {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	op.Origin, op.Seq, op.Stamp = h.name, h.seq, h.clock.Now()
	h.state.Apply(op)
	return op
}

func (h *Host) Set(_ context.Context, table, key string, fields map[string]any) (oplog.Op, error) {
	raw := make(map[string]json.RawMessage, len(fields))
	for name, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return oplog.Op{}, err
		}
		raw[name] = b
	}
	return h.write(oplog.Op{Table: table, Key: key, Fields: raw}), nil
}

func (h *Host) Delete(_ context.Context, table, key string) (oplog.Op, error) {
	return h.write(oplog.Op{Table: table, Key: key, Delete: true}), nil
}

func (h *Host) Get(table, key string) (map[string]json.RawMessage, bool) {
	return h.state.Get(table, key)
}

func (h *Host) Keys(table string) []string { return h.state.Keys(table) }

func (h *Host) NextKey() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keys++
	return fmt.Sprintf("%s:%016x", h.name, h.keys)
}

func (h *Host) Emit(ctx context.Context, e module.Event) error {
	fields := map[string]any{"type": e.Type, "source": e.Source, "at": e.At}
	if len(e.Data) > 0 {
		fields["data"] = e.Data
	}
	_, err := h.Set(ctx, module.EventTable, h.NextKey(), fields)
	return err
}
