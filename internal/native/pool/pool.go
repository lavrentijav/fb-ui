// Package pool keeps a name that resolves to every live master (the panel's
// and the subscriptions' address): each master checks each master the way a
// client would, and one of them, by lease, writes the records.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lease"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

const (
	// MastersTable holds each master's public address, written by the master itself.
	MastersTable = "masters"
	// Table holds what was last written to DNS per name.
	Table      = "dnspool"
	LeaseTable = "poollease"
)

type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

type Pool struct {
	Master *master.Master
	State  Reader
	Name   string // master.fleet.example
	// Check tells whether a master answers on ip as a client would reach it.
	Check func(ctx context.Context, ip string) error
	DNS   func(name string) (dnsprovider.Provider, string, error)
	// Down is how many failed checks in a row drop a master (default 3).
	Down   int
	TTL    int           // DNS record TTL in seconds (default 60)
	Lease  time.Duration // the writer's lease (default 2 min)
	Settle time.Duration // default 5 s
	Logf   func(format string, args ...any)

	mu     sync.Mutex
	failed map[string]int // ip -> failed checks in a row
}

// Announce records this master's public address for the others.
func Announce(ctx context.Context, m *master.Master, state Reader, ip string) error {
	if rec, ok := state.Get(MastersTable, m.Name()); ok {
		var cur string
		if json.Unmarshal(rec["ip"], &cur) == nil && cur == ip {
			return nil
		}
	}
	_, err := m.Set(ctx, MastersTable, m.Name(), map[string]any{"ip": ip})
	return err
}

func (p *Pool) members() []string {
	var ips []string
	for _, name := range p.State.Keys(MastersTable) {
		rec, _ := p.State.Get(MastersTable, name)
		var ip string
		if json.Unmarshal(rec["ip"], &ip) == nil && ip != "" {
			ips = append(ips, ip)
		}
	}
	slices.Sort(ips)
	return slices.Compact(ips)
}

// live checks every master and returns those that have not failed Down
// checks in a row.
func (p *Pool) live(ctx context.Context) []string {
	ips := p.members()
	results := make([]error, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			results[i] = p.Check(cctx, ip)
		})
	}
	wg.Wait()
	down := p.Down
	if down <= 0 {
		down = 3
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed == nil {
		p.failed = map[string]int{}
	}
	var live []string
	for i, ip := range ips {
		if results[i] == nil {
			p.failed[ip] = 0
		} else {
			p.failed[ip]++
		}
		if p.failed[ip] < down {
			live = append(live, ip)
		}
	}
	return live
}

func (p *Pool) written() []string {
	rec, ok := p.State.Get(Table, p.Name)
	if !ok {
		return nil
	}
	var ips []string
	_ = json.Unmarshal(rec["ips"], &ips)
	return ips
}

func (p *Pool) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// Tick checks the masters and, when the live set differs from the records and
// this master holds the writer's lease, writes it. The pool is never emptied:
// with no master answering, clients are better off with the last good records.
func (p *Pool) Tick(ctx context.Context) error {
	live := p.live(ctx)
	l := lease.Lease{
		Master: p.Master, State: p.State, Table: LeaseTable,
		TTL: orDefault(p.Lease, 2*time.Minute), Settle: orDefault(p.Settle, 5*time.Second),
	}
	holder := l.Holder(p.Name)
	if len(live) == 0 {
		return errors.New("pool: no master answers; records left as they are")
	}
	if slices.Equal(live, p.written()) {
		if holder == p.Master.Name() {
			_, err := l.Claim(ctx, p.Name) // the writer keeps its lease while all is well
			return err
		}
		return nil
	}
	ok, err := l.Claim(ctx, p.Name)
	if err != nil || !ok {
		return err
	}
	dns, zone, err := p.DNS(p.Name)
	if err != nil {
		return err
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 60
	}
	if err := dns.SetRecords(ctx, p.Name, "A", live, ttl); err != nil {
		return err
	}
	p.logf("pool: %s (%s) now %v", p.Name, zone, live)
	_, err = p.Master.Set(ctx, Table, p.Name, map[string]any{"ips": live})
	return err
}

// Run ticks every interval until ctx ends.
func (p *Pool) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := p.Tick(ctx); err != nil && ctx.Err() == nil {
			p.logf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Failing lists the addresses with failed checks, for the panel.
func (p *Pool) Failing() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := maps.Clone(p.failed)
	maps.DeleteFunc(out, func(_ string, n int) bool { return n == 0 })
	return out
}
