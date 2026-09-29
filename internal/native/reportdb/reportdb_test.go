package reportdb_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reportdb"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XUI_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set XUI_TEST_PG_DSN")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func send(t *testing.T, b *bus.Bus, master string, seqs ...uint64) {
	t.Helper()
	nc, err := nats.Connect(b.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	for _, seq := range seqs {
		r := reports.Report{
			Node: "nl2", Seq: seq, At: time.Now().UnixMilli(),
			Traffic: []reports.Traffic{{Email: "alice", Up: 100, Down: 1000}},
			Online:  []string{"alice"},
		}
		if err := reports.Send(context.Background(), nc, master, r); err != nil {
			t.Fatalf("send %d to %s: %v", seq, master, err)
		}
	}
}

func waitReports(t *testing.T, buses map[string]*bus.Bus, want map[string]uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		ok := true
		for _, b := range buses {
			for m, n := range want {
				if _, last, err := b.ReportRange(context.Background(), m); err != nil || last != n {
					ok = false
				}
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("report mirrors did not reach %v", want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// nl2 reports 1 and 2 to ru2, loses the ack of 2, fails over to fi1 and
// sends 2 again plus 3: every master counts three reports, not four.
func TestFailoverReportCountedOnceOnEveryMaster(t *testing.T) {
	db := pool(t)
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)

	send(t, buses["ru2"], "ru2", 1, 2)
	send(t, buses["fi1"], "fi1", 2, 3)
	waitReports(t, buses, map[string]uint64{"ru2": 2, "fi1": 2})

	for _, m := range bustest.Masters {
		schema := fmt.Sprintf("r_%s_%d", m, rand.IntN(1<<30))
		t.Cleanup(func() { _, _ = db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
		st, err := reportdb.Open(ctx, db, schema, buses[m], bustest.Masters)
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.CatchUp(ctx)
		if err != nil {
			t.Fatal(m, err)
		}
		if p.Applied != 3 || p.Duplicates != 1 || p.Lost != 0 {
			t.Fatalf("%s progress %+v", m, p)
		}
		up, down, err := st.Total(ctx, "alice")
		if err != nil || up != 300 || down != 3000 {
			t.Fatalf("%s total alice = %d/%d %v, want 300/3000", m, up, down, err)
		}
		online, _ := st.Online(ctx, time.Now().Add(-time.Minute))
		if !slices.Equal(online, []string{"alice"}) {
			t.Fatalf("%s online %v", m, online)
		}
		if again, _ := st.CatchUp(ctx); again != (reportdb.Progress{}) {
			t.Fatalf("%s second catch-up %+v", m, again)
		}
		if _, err := st.PruneSeen(ctx, time.Hour); err == nil {
			t.Fatal("pruning inside the retention window accepted")
		}
	}
}

// A node's newest report sets its state even when an older one, sent to
// another master, is projected after it.
func TestNodeStateKeepsNewestReport(t *testing.T) {
	db := pool(t)
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	publish := func(via string, r reports.Report) {
		t.Helper()
		nc, err := nats.Connect(buses[via].ClientURL())
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		if err := reports.Send(ctx, nc, via, r); err != nil {
			t.Fatal(err)
		}
	}
	publish("ru2", reports.Report{Node: "nl2", Seq: 1, At: time.Now().UnixMilli(), Drain: &reports.Drain{Since: 1790000000, Active: 3}})
	publish("fi1", reports.Report{Node: "nl2", Seq: 2, At: time.Now().UnixMilli()})
	waitReports(t, buses, map[string]uint64{"ru2": 1, "fi1": 1})

	schema := fmt.Sprintf("s_%d", rand.IntN(1<<30))
	t.Cleanup(func() { _, _ = db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	st, err := reportdb.Open(ctx, db, schema, buses["de2"], bustest.Masters) // fi1's stream is projected before ru2's
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.NodeState(ctx, "nl2")
	if err != nil || got.Draining || got.Active != 0 {
		t.Fatalf("state %+v %v: the older draining report overrode the newer one", got, err)
	}
}
