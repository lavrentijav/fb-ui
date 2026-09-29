package pgstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lww"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
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

func store(t *testing.T, db *pgxpool.Pool, name string) *Store {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("t_%s_%d", name, rand.IntN(1<<30))
	s, err := Open(ctx, db, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	return s
}

// history makes per-origin op sequences with colliding walls and deletes.
func history(r *rand.Rand) map[string][]oplog.Op {
	out := map[string][]oplog.Op{}
	for _, origin := range []string{"fi1", "ru2", "de2"} {
		for seq := uint64(1); seq <= 60; seq++ {
			op := oplog.Op{
				Origin: origin, Seq: seq, Stamp: hlc.Stamp{Wall: int64(r.IntN(40)), Logical: uint32(seq), Origin: origin},
				Table: "clients", Key: []string{"a", "b", "c"}[r.IntN(3)],
			}
			if r.IntN(6) == 0 {
				op.Delete = true
			} else {
				op.Fields = map[string]json.RawMessage{
					[]string{"remark", "limitGB", "enable"}[r.IntN(3)]: json.RawMessage(fmt.Sprintf(`{"v":%d}`, r.IntN(100))),
				}
			}
			out[origin] = append(out[origin], op)
		}
	}
	return out
}

// interleave keeps each origin's order, like replication delivers it.
func interleave(r *rand.Rand, h map[string][]oplog.Op) []oplog.Op {
	pos := map[string]int{}
	var out []oplog.Op
	for len(out) < 180 {
		o := []string{"fi1", "ru2", "de2"}[r.IntN(3)]
		if pos[o] < len(h[o]) {
			out = append(out, h[o][pos[o]])
			pos[o]++
		}
	}
	return out
}

// Two masters applying the same streams in different interleavings end up
// with the same rows, and those rows match the in-memory lww reference.
func TestProjectionConvergesAndMatchesLWW(t *testing.T) {
	db := pool(t)
	ctx := context.Background()
	r := rand.New(rand.NewPCG(7, 9))
	h := history(r)
	a, b := store(t, db, "a"), store(t, db, "b")
	ref := lww.New()
	for _, op := range interleave(r, h) {
		if _, err := a.Apply(ctx, op); err != nil {
			t.Fatal(err)
		}
		ref.Apply(op)
	}
	for _, op := range interleave(r, h) {
		if _, err := b.Apply(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	da, _ := a.Digest(ctx)
	db2, _ := b.Digest(ctx)
	if da != db2 {
		t.Fatal("projections diverged")
	}
	keys, _ := a.Keys(ctx, "clients")
	if want := ref.Keys("clients"); fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("keys %v, lww %v", keys, want)
	}
	for _, k := range keys {
		got, _, _ := a.Get(ctx, "clients", k)
		want, _ := ref.Get("clients", k)
		if len(got) != len(want) {
			t.Fatalf("%s: %s vs lww %s", k, got, want)
		}
		for f, v := range want {
			var x, y any
			_ = json.Unmarshal(v, &x)
			_ = json.Unmarshal(got[f], &y)
			if fmt.Sprint(x) != fmt.Sprint(y) {
				t.Fatalf("%s.%s: %s vs lww %s", k, f, got[f], v)
			}
		}
	}
}

func TestApplyOnceAndInOrder(t *testing.T) {
	db := pool(t)
	ctx := context.Background()
	s := store(t, db, "o")
	op := func(seq uint64) oplog.Op {
		return oplog.Op{
			Origin: "ru2", Seq: seq, Stamp: hlc.Stamp{Wall: 100 + int64(seq), Origin: "ru2"}, Table: "t", Key: "k",
			Fields: map[string]json.RawMessage{"n": json.RawMessage(fmt.Sprint(seq))},
		}
	}
	if ok, err := s.Apply(ctx, op(1)); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := s.Apply(ctx, op(1)); ok || err != nil {
		t.Fatalf("re-delivery: %v %v", ok, err)
	}
	if _, err := s.Apply(ctx, op(3)); !errors.Is(err, oplog.ErrGap) {
		t.Fatalf("gap: %v", err)
	}
	v, _ := s.Applied(ctx)
	newest, _ := s.Newest(ctx, "ru2")
	if v["ru2"] != 1 || newest != op(1).Stamp {
		t.Fatalf("applied %v newest %v", v, newest)
	}
}
