package master_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pgstate"
)

type projection interface {
	master.Store
	Digest(ctx context.Context) ([32]byte, error)
}

type memDigest struct{ *master.Mem }

func (m memDigest) Digest(context.Context) ([32]byte, error) { return m.State.Digest(), nil }

func stores(t *testing.T) map[string]func(name string) projection {
	out := map[string]func(string) projection{
		"mem": func(string) projection { return memDigest{master.NewMem()} },
	}
	if dsn := os.Getenv("XUI_TEST_PG_DSN"); dsn != "" {
		db, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		out["postgres"] = func(name string) projection {
			schema := fmt.Sprintf("m_%s_%d", name, rand.IntN(1<<30))
			s, err := pgstate.Open(context.Background(), db, schema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = db.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
			return s
		}
	}
	return out
}

// ok fails the test on a write error: ok(t)(m.Set(...)).
func ok(t *testing.T) func(oplog.Op, error) {
	return func(_ oplog.Op, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Every master takes writes while RU and EU are split; after the heal all
// three projections are identical and causally later writes win.
func TestAnyMasterWritesThroughSplit(t *testing.T) {
	if testing.Short() {
		t.Skip("starts NATS fleets")
	}
	for kind, newStore := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			buses, links := bustest.Fleet(t)
			ms, st := map[string]*master.Master{}, map[string]projection{}
			for _, name := range bustest.Masters {
				st[name] = newStore(name)
				m, err := master.New(ctx, name, bustest.Masters, buses[name], st[name])
				if err != nil {
					t.Fatal(err)
				}
				ms[name] = m
			}

			ok(t)(ms["ru2"].Set(ctx, "clients", "alice", map[string]any{"remark": "alice", "limitGB": 50}))
			bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 1}, time.Minute)

			for k, l := range links {
				if strings.Contains(k, "ru2") {
					l.SetCut(true)
				}
			}
			ok(t)(ms["ru2"].Set(ctx, "clients", "alice", map[string]any{"limitGB": 100}))
			ok(t)(ms["ru2"].Set(ctx, "clients", "carol", map[string]any{"remark": "signed up on ru2"}))
			ok(t)(ms["fi1"].Set(ctx, "nodes", "nl2", map[string]any{"maintenance": true}))
			ok(t)(ms["de2"].Set(ctx, "clients", "alice", map[string]any{"remark": "Alice (vip)"}))
			for _, l := range links {
				l.SetCut(false)
			}
			bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 3, "fi1": 1, "de2": 1}, 2*time.Minute)

			// after seeing everything, fi1 deletes carol: later than her sign-up
			if _, err := ms["fi1"].CatchUp(ctx); err != nil {
				t.Fatal(err)
			}
			ok(t)(ms["fi1"].Delete(ctx, "clients", "carol"))
			bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 3, "fi1": 2, "de2": 1}, time.Minute)

			var digests []string
			for _, name := range bustest.Masters {
				if _, err := ms[name].CatchUp(ctx); err != nil {
					t.Fatal(name, err)
				}
				d, err := st[name].Digest(ctx)
				if err != nil {
					t.Fatal(err)
				}
				digests = append(digests, fmt.Sprintf("%x", d[:6]))
			}
			if digests[0] != digests[1] || digests[1] != digests[2] {
				t.Fatalf("projections differ: %v", digests)
			}
			if mem, ok := st["de2"].(memDigest); ok {
				alice, _ := mem.State.Get("clients", "alice")
				if string(alice["remark"]) != `"Alice (vip)"` || string(alice["limitGB"]) != "100" {
					t.Fatalf("alice = %s: both split-side edits must survive", alice)
				}
				if _, ok := mem.State.Get("clients", "carol"); ok {
					t.Fatal("carol survived a later delete")
				}
			}
		})
	}
}

// A master restarted on the same stream and store continues numbering and
// never stamps before its last op, even with the host clock behind.
func TestRestartContinues(t *testing.T) {
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	store := master.NewMem()
	m, err := master.New(ctx, "ru2", bustest.Masters, buses["ru2"], store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Set(ctx, "clients", "a", map[string]any{"x": 1})
	ok(t)(first, err)

	restore := master.SetWallClock(func() int64 { return first.Stamp.Wall - 60_000 })
	defer restore()
	m, err = master.New(ctx, "ru2", bustest.Masters, buses["ru2"], store)
	if err != nil {
		t.Fatal(err)
	}
	next, err := m.Set(ctx, "clients", "a", map[string]any{"x": 2})
	ok(t)(next, err)
	if next.Seq != 2 || !next.Stamp.After(first.Stamp) {
		t.Fatalf("after restart seq=%d stamp %v, first %v", next.Seq, next.Stamp, first.Stamp)
	}
}
