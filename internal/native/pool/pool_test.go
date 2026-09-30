package pool_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pool"
)

type dns struct {
	mu     sync.Mutex
	writes [][]string
}

func (d *dns) SetRecords(_ context.Context, _, _ string, values []string, _ int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, slices.Clone(values))
	return nil
}
func (d *dns) DeleteRecords(context.Context, string, string) error { return nil }
func (d *dns) Check(context.Context) error                         { return nil }

func (d *dns) last() ([]string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.writes) == 0 {
		return nil, 0
	}
	return d.writes[len(d.writes)-1], len(d.writes)
}

type net struct {
	mu   sync.Mutex
	down map[string]bool
}

func (n *net) check(_ context.Context, ip string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.down[ip] {
		return errors.New("no answer")
	}
	return nil
}

func (n *net) set(ip string, down bool) {
	n.mu.Lock()
	n.down[ip] = down
	n.mu.Unlock()
}

var ips = map[string]string{"fi1": "10.0.1.1", "ru2": "10.0.1.2", "de2": "10.0.1.3"}

func TestPoolFollowsLiveMasters(t *testing.T) {
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	d, n := &dns{}, &net{down: map[string]bool{}}
	pools := map[string]*pool.Pool{}
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Announce(ctx, m, store.State, ips[name]); err != nil {
			t.Fatal(err)
		}
		pools[name] = &pool.Pool{
			Master: m, State: store.State, Name: "master.firewalk.top", Check: n.check,
			DNS:    func(string) (dnsprovider.Provider, string, error) { return d, "firewalk.top", nil },
			Settle: 200 * time.Millisecond,
		}
	}
	tickAll := func() {
		t.Helper()
		var wg sync.WaitGroup
		for _, p := range pools {
			wg.Go(func() { _ = p.Tick(ctx) })
		}
		wg.Wait()
		for _, p := range pools {
			_, _ = p.Master.CatchUp(ctx)
		}
	}
	all := []string{"10.0.1.1", "10.0.1.2", "10.0.1.3"}
	deadline := time.Now().Add(20 * time.Second)
	for got, _ := d.last(); !slices.Equal(got, all); got, _ = d.last() {
		if time.Now().After(deadline) {
			t.Fatalf("records %v, want %v", got, all)
		}
		tickAll() // the masters' addresses replicate first
	}
	_, writes := d.last()
	tickAll()
	tickAll()
	if _, now := d.last(); now != writes {
		t.Fatalf("unchanged pool written again (%d -> %d)", writes, now)
	}

	n.set("10.0.1.3", true)
	tickAll()
	tickAll()
	if got, _ := d.last(); !slices.Equal(got, all) {
		t.Fatalf("dropped after two failed checks: %v", got)
	}
	tickAll()
	if got, _ := d.last(); !slices.Equal(got, all[:2]) {
		t.Fatalf("after three failed checks: %v", got)
	}

	for _, ip := range all {
		n.set(ip, true)
	}
	for range 4 {
		tickAll()
	}
	if got, _ := d.last(); !slices.Equal(got, all[:2]) {
		t.Fatalf("no master answering emptied or changed the pool: %v", got)
	}

	for _, ip := range all {
		n.set(ip, false)
	}
	tickAll()
	if got, _ := d.last(); !slices.Equal(got, all) {
		t.Fatalf("recovered masters not back: %v", got)
	}
}
