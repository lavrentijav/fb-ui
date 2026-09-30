package dnsacct_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/dnsacct"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

// zones answers per credential, as each provider account would.
type zones struct {
	mu  sync.Mutex
	by  map[string][]string
	hit int
}

func (z *zones) discover(_ context.Context, cfg dnsprovider.Config) ([]string, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.hit++
	got, ok := z.by[cfg.Credential]
	if !ok {
		return nil, errors.New("bad token")
	}
	return append([]string(nil), got...), nil
}

func (z *zones) set(cred string, list ...string) {
	z.mu.Lock()
	z.by[cred] = list
	z.mu.Unlock()
}

func accounts(t *testing.T, z *zones) map[string]*dnsacct.Accounts {
	t.Helper()
	buses, _ := bustest.Fleet(t)
	out := map[string]*dnsacct.Accounts{}
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(context.Background(), name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &dnsacct.Accounts{Master: m, State: store.State, Discover: z.discover, ResyncEvery: time.Hour}
	}
	return out
}

func zoneOf(t *testing.T, a *dnsacct.Accounts, name string) string {
	t.Helper()
	_, zone, err := a.For(name)
	if err != nil {
		return err.Error()
	}
	return zone
}

func TestAccountsPickTheLongestZoneOnEveryMaster(t *testing.T) {
	ctx := context.Background()
	z := &zones{by: map[string][]string{"main": {"fleet.example"}, "ru": {"ru.fleet.example"}}}
	accs := accounts(t, z)

	if _, err := accs["ru2"].Put(ctx, "main", dnsacct.Account{Type: dnsprovider.TypeTechnitium, BaseURL: "http://dns", Credential: "wrong"}); err == nil {
		t.Fatal("an account whose credential lists no zones was saved")
	}
	for id, cred := range map[string]string{"main": "main", "ru": "ru"} {
		if _, err := accs["ru2"].Put(ctx, id, dnsacct.Account{Type: dnsprovider.TypeTechnitium, BaseURL: "http://dns", Credential: cred}); err != nil {
			t.Fatal(err)
		}
	}
	fi1 := accs["fi1"]
	deadline := time.Now().Add(20 * time.Second)
	for len(fi1.List()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("accounts did not reach fi1")
		}
		_, _ = fi1.Master.CatchUp(ctx)
		time.Sleep(100 * time.Millisecond)
	}
	for name, want := range map[string]string{
		"_acme-challenge.ru.fleet.example": "ru.fleet.example", // delegated sub-zone wins
		"_acme-challenge.de.fleet.example": "fleet.example",
	} {
		if got := zoneOf(t, fi1, name); got != want {
			t.Errorf("%s: zone %q, want %q", name, got, want)
		}
	}
	for id, acc := range fi1.List() {
		if acc.Credential != "" {
			t.Errorf("List shows %s's credential", id)
		}
	}

	// a zone added at the provider later is found on the first miss
	z.set("main", "fleet.example", "home.example")
	if got := zoneOf(t, fi1, "_acme-challenge.alko.home.example"); got != "home.example" {
		t.Fatalf("new zone: got %q", got)
	}
	hits := z.hit
	if got := zoneOf(t, fi1, "_acme-challenge.example.org"); got == "example.org" {
		t.Fatal("found a zone no account holds")
	}
	if z.hit != hits {
		t.Fatalf("a second miss within ResyncEvery asked the providers again (%d calls)", z.hit-hits)
	}
}
