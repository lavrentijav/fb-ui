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
		if err := pool.Announce(ctx, m, store.State, []string{ips[name]}); err != nil {
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
	// the third failure makes every master call it down; the fleet needs the
	// verdicts to replicate before the writer acts on them
	for range 5 {
		tickAll()
	}
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
	// the fleet decides by majority: the first round publishes the new
	// verdicts, the next one sees them everywhere
	for range 3 {
		tickAll()
	}
	if got, _ := d.last(); !slices.Equal(got, all) {
		t.Fatalf("recovered masters not back: %v", got)
	}
}

// oneMaster builds a single master's pool over an in-memory bus.
func oneMaster(t *testing.T, name string, addrs []string, d *dns, n *net) (context.Context, *pool.Pool, func()) {
	t.Helper()
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	store := master.NewMem()
	m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Announce(ctx, m, store.State, addrs); err != nil {
		t.Fatal(err)
	}
	p := &pool.Pool{
		Master: m, State: store.State, Name: "master.firewalk.top", Check: n.check,
		DNS:    func(string) (dnsprovider.Provider, string, error) { return d, "firewalk.top", nil },
		Settle: 100 * time.Millisecond,
	}
	tick := func() {
		_ = p.Tick(ctx)
		_, _ = m.CatchUp(ctx)
	}
	return ctx, p, tick
}

// A master may be reached at several addresses; they all go into DNS, and a
// blocked one leaves while the others stay.
func TestManyAddressesPerMaster(t *testing.T) {
	d, n := &dns{}, &net{down: map[string]bool{}}
	addrs := []string{"10.0.2.1", "10.0.2.2", "10.0.2.3"}
	_, _, tick := oneMaster(t, "fi1", addrs, d, n)
	for range 3 {
		tick()
	}
	if got, _ := d.last(); !slices.Equal(got, addrs) {
		t.Fatalf("records %v, want %v", got, addrs)
	}
	// after Down failures it moves to the next two
	n.set("10.0.2.1", true)
	for range 3 {
		tick()
	}
	if got, _ := d.last(); !slices.Equal(got, addrs[1:]) {
		t.Fatalf("blocked address still advertised: %v", got)
	}
	// and comes back when it answers again
	n.set("10.0.2.1", false)
	for range 2 {
		tick()
	}
	if got, _ := d.last(); !slices.Equal(got, addrs) {
		t.Fatalf("recovered address not back: %v", got)
	}
}

// One master whose route to an address is blocked must not move everybody off
// it: the fleet decides by share, and 1 of 3 is not enough.
func TestOneBlockedMasterDoesNotDropAnAddress(t *testing.T) {
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	d := &dns{}
	addrs := map[string][]string{"fi1": {"10.0.3.1"}, "ru2": {"10.0.3.2"}, "de2": {"10.0.3.3"}}
	down := map[string]map[string]bool{"fi1": {"10.0.3.3": true}} // only fi1 cannot reach de2
	pools := map[string]*pool.Pool{}
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Announce(ctx, m, store.State, addrs[name]); err != nil {
			t.Fatal(err)
		}
		who := name
		check := func(_ context.Context, ip string) error {
			if down[who][ip] {
				return errors.New("blocked here")
			}
			return nil
		}
		pools[name] = &pool.Pool{
			Master: m, State: store.State, Name: "master.firewalk.top", Check: check,
			DNS:    func(string) (dnsprovider.Provider, string, error) { return d, "firewalk.top", nil },
			Settle: 100 * time.Millisecond, HealthTTL: time.Minute,
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
	all := []string{"10.0.3.1", "10.0.3.2", "10.0.3.3"}
	// wait for the addresses to replicate to every master before deciding
	deadline := time.Now().Add(20 * time.Second)
	for got, _ := d.last(); !slices.Equal(got, all); got, _ = d.last() {
		if time.Now().After(deadline) {
			t.Fatalf("baseline records %v, want %v", got, all)
		}
		tickAll()
	}
	// one master out of three failing is not a majority: the address stays
	for range 6 {
		tickAll()
	}
	if got, _ := d.last(); !slices.Equal(got, all) {
		t.Fatalf("one blocked master moved the fleet off %v: %v", all, got)
	}

	// now two of three fail: it leaves after enough rounds for the verdicts
	// to replicate to whoever writes DNS
	down["ru2"] = map[string]bool{"10.0.3.3": true}
	for range 8 {
		tickAll()
	}
	if got, _ := d.last(); !slices.Equal(got, all[:2]) {
		t.Fatalf("a majority down did not drop the address: %v", got)
	}
}

// MaxRecords caps the answer and keeps the next live address ready, so a
// blocked address is replaced instead of shrinking the list.
func TestMaxRecordsKeepsTheNextAddressReady(t *testing.T) {
	d, n := &dns{}, &net{down: map[string]bool{}}
	addrs := []string{"10.0.4.1", "10.0.4.2", "10.0.4.3"}
	_, p, tick := oneMaster(t, "fi1", addrs, d, n)
	p.MaxRecords = 2
	for range 3 {
		tick()
	}
	if got, _ := d.last(); !slices.Equal(got, addrs[:2]) {
		t.Fatalf("cap not applied: %v", got)
	}
	// the blocked one is replaced by the next live address, not dropped
	n.set("10.0.4.1", true)
	for range 5 {
		tick()
	}
	if got, _ := d.last(); !slices.Equal(got, addrs[1:]) {
		t.Fatalf("next address did not fill the place: %v", got)
	}
}

// An operator's list is not overwritten by the master's own detection.
func TestManualAddressesSurviveAutoDetection(t *testing.T) {
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	store := master.NewMem()
	m, err := master.New(ctx, "fi1", bustest.Masters, buses["fi1"], store)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.SetManual(ctx, m, store.State, []string{"10.0.5.1", "10.0.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.AnnounceAuto(ctx, m, store.State, []string{"10.9.9.9"}); err != nil {
		t.Fatal(err)
	}
	got, manual := pool.Manual(store.State, "fi1")
	if !manual || !slices.Equal(got, []string{"10.0.5.1", "10.0.5.2"}) {
		t.Fatalf("manual list lost: %v %v", got, manual)
	}
	// an explicit non-manual announce clears it
	if err := pool.Announce(ctx, m, store.State, []string{"10.0.5.3"}); err != nil {
		t.Fatal(err)
	}
	if _, manual := pool.Manual(store.State, "fi1"); manual {
		t.Fatal("the manual flag stuck")
	}
}

func TestFailoverStopsAtTheFirstAnswer(t *testing.T) {
	var tried []string
	err := pool.Failover(context.Background(), []string{"a", "b", "c"}, func(_ context.Context, ip string) error {
		tried = append(tried, ip)
		if ip != "c" {
			return errors.New("unreachable")
		}
		return nil
	})
	if err != nil || !slices.Equal(tried, []string{"a", "b", "c"}) {
		t.Fatalf("failover %v %v", tried, err)
	}
}
