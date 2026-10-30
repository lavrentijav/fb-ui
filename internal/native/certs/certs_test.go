package certs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

type fleet struct {
	masters  map[string]*master.Master
	renewers map[string]*certs.Renewer
	issued   atomic.Int32
	now      atomic.Int64
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	f := &fleet{masters: map[string]*master.Master{}, renewers: map[string]*certs.Renewer{}}
	f.now.Store(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		f.masters[name] = m
		f.renewers[name] = &certs.Renewer{
			Master: m, State: store.State, Settle: 3 * time.Second,
			Now: func() time.Time { return time.UnixMilli(f.now.Load()) },
			Issue: func(_ context.Context, names []string) (certs.Issued, error) {
				n := f.issued.Add(1)
				return certs.Issued{
					CertPEM: fmt.Sprintf("cert-%d-by-%s", n, name), KeyPEM: "key",
					NotAfter: time.UnixMilli(f.now.Load()).Add(90 * 24 * time.Hour),
				}, nil
			},
		}
	}
	return f
}

// every master ticks at once, as their renewal loops would
func (f *fleet) tickAll(t *testing.T) {
	t.Helper()
	var wg sync.WaitGroup
	for _, r := range f.renewers {
		wg.Go(func() {
			if _, err := r.Tick(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, m := range f.masters {
		if _, err := m.CatchUp(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func certOn(f *fleet, name, id string) string {
	got := certs.Resolve(f.renewers[name].State, []string{id})
	if len(got) == 0 {
		return ""
	}
	return got[0].CertPEM
}

// Three masters renew concurrently; the lease makes exactly one issue,
// and the certificate reaches all of them.
func TestOneMasterIssuesAndAllGetIt(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	if err := certs.Want(ctx, f.masters["ru2"], "ru-group", []string{"ru.firewalk.top", "*.ru.firewalk.top"}); err != nil {
		t.Fatal(err)
	}
	waitAll(t, f, func(name string) bool { return len(f.renewers[name].State.Keys(certs.Table)) == 1 })

	f.tickAll(t)
	if n := f.issued.Load(); n != 1 {
		t.Fatalf("issued %d times, want once", n)
	}
	waitAll(t, f, func(name string) bool { return certOn(f, name, "ru-group") != "" })
	first := certOn(f, "fi1", "ru-group")
	for name := range f.masters {
		if got := certOn(f, name, "ru-group"); got != first {
			t.Fatalf("%s has %q, fi1 has %q", name, got, first)
		}
	}

	f.tickAll(t)
	if n := f.issued.Load(); n != 1 {
		t.Fatalf("a fresh certificate was issued again (%d)", n)
	}

	f.now.Add((61 * 24 * time.Hour).Milliseconds()) // 29 days left
	f.tickAll(t)
	if n := f.issued.Load(); n != 2 {
		t.Fatalf("expiring certificate: issued %d, want a second time", n)
	}

	if err := certs.Want(ctx, f.masters["de2"], "ru-group", []string{"ru.firewalk.top", "*.ru.firewalk.top", "ru7.firewalk.top"}); err != nil {
		t.Fatal(err)
	}
	waitAll(t, f, func(name string) bool {
		rec, _ := f.renewers[name].State.Get(certs.Table, "ru-group")
		return len(rec["names"]) > 40
	})
	f.tickAll(t)
	if n := f.issued.Load(); n != 3 {
		t.Fatalf("names changed: issued %d, want a third time", n)
	}
}

func waitAll(t *testing.T, f *fleet, ok func(name string) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		all := true
		for name, m := range f.masters {
			_, _ = m.CatchUp(context.Background())
			if !ok(name) {
				all = false
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("masters did not converge")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestCovers(t *testing.T) {
	names := []string{"ru.firewalk.top", "*.ru.firewalk.top"}
	for host, want := range map[string]bool{
		"ru.firewalk.top":      true,
		"RU4.ru.firewalk.top":  true,
		"a.b.ru.firewalk.top":  false, // a wildcard covers one label only
		"ru4.firewalk.top":     false,
		"evilru.firewalk.top":  false,
		"xru.firewalk.top.com": false,
	} {
		if got := certs.Covers(names, host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

// The node's own certificate holds only what the group one does not, a
// repeated save keeps it as it is, and it goes once the group covers all.
func TestWantForNode(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	m, state := f.masters["ru2"], f.renewers["ru2"].State
	if err := certs.Want(ctx, m, "ru-group", []string{"ru.firewalk.top", "*.ru.firewalk.top"}); err != nil {
		t.Fatal(err)
	}
	terminated := []string{"ru.firewalk.top", "ru4.firewalk.top", "Panel.ru4.firewalk.top"}
	names := func() string {
		rec, ok := state.Get(certs.Table, certs.NodeID("ru4"))
		if !ok {
			return "none"
		}
		return string(rec["names"])
	}
	if err := certs.WantForNode(ctx, m, state, "ru4", terminated, nil, []string{"ru-group"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != `["panel.ru4.firewalk.top","ru4.firewalk.top"]` {
		t.Fatalf("node certificate for %s", got)
	}
	before, _ := m.Version(ctx)
	if err := certs.WantForNode(ctx, m, state, "ru4", terminated, nil, []string{"ru-group"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := m.Version(ctx); after != before {
		t.Fatal("saving the same spec wrote the node certificate again")
	}
	if err := certs.WantForNode(ctx, m, state, "ru4", []string{"a.ru.firewalk.top"}, nil, []string{"ru-group"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != "none" {
		t.Fatalf("node certificate kept for %s", got)
	}
}

// A renewal keeps the pair it replaced, so a bad one can be rolled back.
func TestRenewKeepsThePreviousPair(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	if err := certs.Want(ctx, f.masters["ru2"], "g", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	waitAll(t, f, func(name string) bool { return len(f.renewers[name].State.Keys(certs.Table)) == 1 })
	f.tickAll(t)
	waitAll(t, f, func(name string) bool { return certOn(f, name, "g") != "" })
	first := certOn(f, "fi1", "g")
	f.now.Add((61 * 24 * time.Hour).Milliseconds())
	f.tickAll(t)
	waitAll(t, f, func(name string) bool { return certOn(f, name, "g") != first })
	rec, _ := f.renewers["fi1"].State.Get(certs.Table, "g")
	var prev string
	_ = json.Unmarshal(rec["prevCertPem"], &prev)
	if prev != first {
		t.Fatalf("previous pair = %q, want the first one %q", prev, first)
	}
}
