// Package mhost is module.Host over a real master: writes go through the op
// log, reads come from the projection, and "leader" is a lease in the op log.
package mhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/lease"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lww"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// LeaseTable and LeaderKey name the lease that elects the master doing the
// jobs that must happen once.
const (
	LeaseTable = "lease"
	LeaderKey  = "leader"
)

// Options tune the leader lease; zero values give the production settings.
type Options struct {
	Now      func() time.Time
	LeaseTTL time.Duration // default 30 s
	Settle   time.Duration // default 2 s: how long a claim replicates before it is checked
	Recheck  time.Duration // default 5 s: how long a Leader answer is reused
}

type Host struct {
	m     *master.Master
	state *lww.State
	opt   Options

	mu      sync.Mutex
	checked time.Time
	leader  bool
	lastKey int64
}

func New(m *master.Master, state *lww.State, opt Options) *Host {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.LeaseTTL == 0 {
		opt.LeaseTTL = 30 * time.Second
	}
	if opt.Settle == 0 {
		opt.Settle = 2 * time.Second
	}
	if opt.Recheck == 0 {
		opt.Recheck = 5 * time.Second
	}
	return &Host{m: m, state: state, opt: opt}
}

func (h *Host) name() string {
	if h.m == nil {
		return ""
	}
	return h.m.Name()
}

func (h *Host) Name() string   { return h.name() }
func (h *Host) Now() time.Time { return h.opt.Now() }

func (h *Host) Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error) {
	return h.m.Set(ctx, table, key, fields)
}

func (h *Host) Delete(ctx context.Context, table, key string) (oplog.Op, error) {
	return h.m.Delete(ctx, table, key)
}

func (h *Host) Get(table, key string) (map[string]json.RawMessage, bool) {
	return h.state.Get(table, key)
}

func (h *Host) Keys(table string) []string { return h.state.Keys(table) }

// NextKey is monotonic per master, so keys sort by creation order there.
func (h *Host) NextKey() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.opt.Now().UnixMicro()
	if v <= h.lastKey {
		v = h.lastKey + 1
	}
	h.lastKey = v
	return fmt.Sprintf("%s:%016x", h.name(), v)
}

func (h *Host) Emit(ctx context.Context, e module.Event) error {
	fields := map[string]any{"type": e.Type, "source": e.Source, "at": e.At}
	if len(e.Data) > 0 {
		fields["data"] = e.Data
	}
	_, err := h.Set(ctx, module.EventTable, h.NextKey(), fields)
	return err
}

// Leader claims or renews the leader lease at most once per Recheck. A master
// that cannot reach the op log is not the leader.
func (h *Host) Leader(ctx context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now := h.opt.Now(); !h.checked.IsZero() && now.Sub(h.checked) < h.opt.Recheck {
		return h.leader
	}
	ok, err := lease.Lease{
		Master: h.m, State: h.state, Table: LeaseTable, Now: h.opt.Now,
		TTL: h.opt.LeaseTTL, Settle: h.opt.Settle,
	}.Claim(ctx, LeaderKey)
	h.checked = h.opt.Now()
	h.leader = ok && err == nil
	return h.leader
}
