// Package master is one master of the fleet: it stamps and publishes local
// writes, and projects every master's op stream into its own store.
package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Store is the projection: pgstate on real masters, Mem in tests and tools.
type Store interface {
	Apply(ctx context.Context, op oplog.Op) (bool, error)
	Applied(ctx context.Context) (oplog.Vector, error)
	Newest(ctx context.Context, origin string) (hlc.Stamp, error)
}

type Master struct {
	name    string
	masters []string
	bus     *bus.Bus
	store   Store
	clock   *hlc.Clock

	writeMu sync.Mutex
	nextSeq uint64 // 0 = read from the stream on next write
	catchMu sync.Mutex
}

// wallClock is the host clock; tests replace it.
var wallClock = func() int64 { return time.Now().UnixMilli() }

// New continues the clock after anything this master stamped before,
// whether or not it had reached the store when the process stopped.
func New(ctx context.Context, name string, masters []string, b *bus.Bus, store Store) (*Master, error) {
	m := &Master{name: name, masters: masters, bus: b, store: store, clock: hlc.NewWithTime(name, func() int64 { return wallClock() })}
	newest, err := store.Newest(ctx, name)
	if err != nil {
		return nil, err
	}
	m.clock.Observe(newest)
	heads, err := b.Heads(ctx, masters)
	if err != nil {
		return nil, err
	}
	for origin, seq := range heads {
		op, err := b.Get(ctx, origin, seq)
		if err != nil {
			return nil, fmt.Errorf("master: last op of %s: %w", origin, err)
		}
		m.clock.Observe(op.Stamp)
	}
	return m, nil
}

func (m *Master) Name() string { return m.name }

// Version is the newest stamp this master has projected from any origin;
// a node never accepts a state rendered from an older one.
func (m *Master) Version(ctx context.Context) (hlc.Stamp, error) {
	var v hlc.Stamp
	for _, origin := range m.masters {
		s, err := m.store.Newest(ctx, origin)
		if err != nil {
			return hlc.Stamp{}, err
		}
		if s.After(v) {
			v = s
		}
	}
	return v, nil
}

// Set writes fields of one record; values are marshalled to JSON.
func (m *Master) Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error) {
	raw := make(map[string]json.RawMessage, len(fields))
	for name, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return oplog.Op{}, fmt.Errorf("master: field %s: %w", name, err)
		}
		raw[name] = b
	}
	return m.write(ctx, oplog.Op{Table: table, Key: key, Fields: raw})
}

func (m *Master) Delete(ctx context.Context, table, key string) (oplog.Op, error) {
	return m.write(ctx, oplog.Op{Table: table, Key: key, Delete: true})
}

// write publishes first: the stream is the source of truth, the store
// catches up from it if this process dies in between.
func (m *Master) write(ctx context.Context, op oplog.Op) (oplog.Op, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if m.nextSeq == 0 {
		heads, err := m.bus.Heads(ctx, []string{m.name})
		if err != nil {
			return oplog.Op{}, err
		}
		m.nextSeq = heads[m.name] + 1
	}
	op.Origin, op.Seq, op.Stamp = m.name, m.nextSeq, m.clock.Now()
	if err := m.bus.Publish(ctx, op); err != nil {
		m.nextSeq = 0
		return oplog.Op{}, err
	}
	m.nextSeq++
	if _, err := m.CatchUp(ctx); err != nil {
		return op, fmt.Errorf("master: published but not projected yet: %w", err)
	}
	return op, nil
}

// CatchUp applies every op the streams hold beyond what the store has.
func (m *Master) CatchUp(ctx context.Context) (int, error) {
	m.catchMu.Lock()
	defer m.catchMu.Unlock()
	heads, err := m.bus.Heads(ctx, m.masters)
	if err != nil {
		return 0, err
	}
	applied, err := m.store.Applied(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, origin := range m.masters {
		for seq := applied[origin] + 1; seq <= heads[origin]; seq++ {
			op, err := m.bus.Get(ctx, origin, seq)
			if err != nil {
				return n, err
			}
			if _, err := m.store.Apply(ctx, op); err != nil {
				return n, err
			}
			m.clock.Observe(op.Stamp)
			n++
		}
	}
	return n, nil
}

// Run catches up every interval until ctx ends.
func (m *Master) Run(ctx context.Context, every time.Duration, onErr func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := m.CatchUp(ctx); err != nil && onErr != nil && !errors.Is(err, context.Canceled) {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
