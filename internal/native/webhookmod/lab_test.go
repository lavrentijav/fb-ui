package webhookmod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/mhost"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhookmod"
)

// Three masters share one op log. A webhook set up through one of them gets
// each event exactly once, from whichever master leads; when the leader goes
// away the next event is delivered by another, and nothing is sent twice.
func TestWebhookDeliveredOnceAcrossMasters(t *testing.T) {
	if testing.Short() {
		t.Skip("starts NATS fleets")
	}
	t.Cleanup(webhookmod.SetURLCheck(func(string) error { return nil }))
	ctx := context.Background()

	var cmu sync.Mutex
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	advance := func(d time.Duration) { cmu.Lock(); clock = clock.Add(d); cmu.Unlock() }

	var rmu sync.Mutex
	var deliveries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rmu.Lock()
		deliveries = append(deliveries, r.Header.Get("X-Fbui-Delivery"))
		rmu.Unlock()
	}))
	defer srv.Close()
	seen := func() []string { rmu.Lock(); defer rmu.Unlock(); return append([]string(nil), deliveries...) }

	buses, _ := bustest.Fleet(t)
	masters := map[string]*master.Master{}
	hosts := map[string]*mhost.Host{}
	mods := map[string]*webhookmod.Module{}
	for _, name := range bustest.Masters {
		store := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], store)
		if err != nil {
			t.Fatal(err)
		}
		masters[name] = m
		hosts[name] = mhost.New(m, store.State, mhost.Options{Now: now, LeaseTTL: 30 * time.Second, Settle: 2 * time.Second, Recheck: time.Millisecond})
		mods[name] = webhookmod.New()
	}
	catchUp := func() {
		t.Helper()
		for _, m := range masters {
			if _, err := m.CatchUp(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	tickAll := func(skip string) {
		t.Helper()
		var wg sync.WaitGroup
		for name := range hosts {
			if name == skip {
				continue
			}
			wg.Go(func() {
				if _, err := mods[name].Tick(ctx, hosts[name]); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			})
		}
		wg.Wait()
		catchUp()
	}

	// the webhook is created through de2, the event through ru2
	if _, err := hosts["de2"].Set(ctx, webhookmod.Table, "hook1", map[string]any{
		"name": "lab", "url": srv.URL, "events": []string{}, "enable": true, "secret": "",
	}); err != nil {
		t.Fatal(err)
	}
	catchUp()
	tickAll("") // the leader sets the cursor; nothing is sent
	if got := seen(); len(got) != 0 {
		t.Fatalf("a new webhook was sent history: %v", got)
	}

	emit := func(via, typ string) {
		t.Helper()
		advance(time.Second)
		if err := hosts[via].Emit(ctx, module.Event{Type: typ, At: now().UnixMilli(), Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		catchUp()
	}
	emit("ru2", "node.down")
	tickAll("")
	tickAll("") // a second pass must not repeat it
	if got := seen(); len(got) != 1 {
		t.Fatalf("one event reached the receiver %d times: %v", len(got), got)
	}

	var leader string
	for name, h := range hosts {
		if h.Leader(ctx) {
			leader = name
		}
	}
	if leader == "" {
		t.Fatal("no leader")
	}
	// the leader goes away: the lease runs out and another master takes over
	advance(31 * time.Second)
	emit("fi1", "node.up")
	if leader == "fi1" {
		emit("de2", "node.up")
	}
	tickAll(leader)
	tickAll(leader)
	got := seen()
	if len(got) < 2 {
		t.Fatalf("the new leader delivered nothing: %v", got)
	}
	uniq := map[string]bool{}
	for _, d := range got {
		if uniq[d] {
			t.Fatalf("delivery %s was sent twice: %v", d, got)
		}
		uniq[d] = true
	}
}
