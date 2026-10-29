package mhost_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/mhost"
)

type fleet struct {
	hosts   map[string]*mhost.Host
	masters map[string]*master.Master
	now     func() time.Time
}

func newFleet(t *testing.T, now func() time.Time) *fleet {
	t.Helper()
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	f := &fleet{hosts: map[string]*mhost.Host{}, masters: map[string]*master.Master{}, now: now}
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		f.masters[name] = m
		f.hosts[name] = mhost.New(m, store.State, mhost.Options{
			Now: now, LeaseTTL: 30 * time.Second, Settle: 2 * time.Second, Recheck: time.Millisecond,
		})
	}
	return f
}

func (f *fleet) leaders(t *testing.T, skip string) []string {
	t.Helper()
	var mu sync.Mutex
	var out []string
	var wg sync.WaitGroup
	for name, h := range f.hosts {
		if name == skip {
			continue
		}
		wg.Go(func() {
			if h.Leader(context.Background()) {
				mu.Lock()
				out = append(out, name)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for _, m := range f.masters {
		if _, err := m.CatchUp(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Of three masters asking at once, one is the leader; it stays the leader;
// when it stops renewing past the TTL, another takes over.
func TestLeaderIsExclusive(t *testing.T) {
	if testing.Short() {
		t.Skip("starts NATS fleets")
	}
	var clock = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	f := newFleet(t, now)

	first := f.leaders(t, "")
	if len(first) != 1 {
		t.Fatalf("leaders after a simultaneous ask: %v", first)
	}
	if again := f.leaders(t, ""); len(again) != 1 || again[0] != first[0] {
		t.Fatalf("the leader changed without cause: %v then %v", first, again)
	}
	advance(31 * time.Second)
	next := f.leaders(t, first[0])
	if len(next) != 1 || next[0] == first[0] {
		t.Fatalf("after the TTL another master should lead: %v (was %v)", next, first)
	}
}

func TestNextKeyIsUniqueAndSortsByCreation(t *testing.T) {
	m := mhost.New(nil, nil, mhost.Options{})
	a, b := m.NextKey(), m.NextKey()
	if a == b || a > b {
		t.Fatalf("keys %q %q", a, b)
	}
}
