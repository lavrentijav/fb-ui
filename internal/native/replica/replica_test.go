package replica

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// tick gives every write its own millisecond, in call order.
func tick(t *testing.T) {
	old, now := wallClock, int64(1_000_000)
	wallClock = func() int64 { now++; return now }
	t.Cleanup(func() { wallClock = old })
}

func open(t *testing.T, id, dir string) *Replica {
	t.Helper()
	r, err := Open(id, dir, oplog.Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func must(t *testing.T, _ oplog.Op, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sync(t *testing.T, to, from *Replica) {
	t.Helper()
	if _, err := to.SyncFrom(from, 3); err != nil {
		t.Fatal(err)
	}
}

// fi1, ru2 and de2 keep taking writes while RU and EU are split, then heal
// through ru2 alone and agree.
func TestMastersWriteDuringSplitAndConverge(t *testing.T) {
	tick(t)
	fi1, ru2, de2 := open(t, "fi1", t.TempDir()), open(t, "ru2", t.TempDir()), open(t, "de2", t.TempDir())

	o, err := ru2.Set("clients", "alice", map[string]any{"remark": "alice", "limitGB": 50})
	must(t, o, err)
	sync(t, fi1, ru2)
	sync(t, de2, ru2)

	// split: ru2 alone | fi1 + de2
	o, err = ru2.Set("clients", "alice", map[string]any{"limitGB": 100})
	must(t, o, err)
	o, err = ru2.Set("clients", "carol", map[string]any{"remark": "new on ru2"})
	must(t, o, err)
	o, err = fi1.Set("clients", "alice", map[string]any{"remark": "Alice (vip)"})
	must(t, o, err)
	sync(t, de2, fi1) // de2 sees the rename, then deletes: the delete is causally later
	o, err = de2.Delete("clients", "alice")
	must(t, o, err)
	o, err = de2.Set("clients", "alice", map[string]any{"enable": false})
	must(t, o, err)
	sync(t, fi1, de2)
	sync(t, de2, fi1)

	// heal only via ru2: fi1 <-> ru2 <-> de2
	sync(t, ru2, fi1)
	sync(t, de2, ru2)
	sync(t, fi1, ru2)

	if fi1.State().Digest() != ru2.State().Digest() || ru2.State().Digest() != de2.State().Digest() {
		t.Fatal("masters diverged after heal")
	}
	alice, ok := ru2.State().Get("clients", "alice")
	// de2 deleted after seeing fi1's rename and after ru2's limit; only its later write survives.
	if !ok || len(alice) != 1 || string(alice["enable"]) != "false" {
		t.Fatalf("alice = %s", alice)
	}
	if _, ok := fi1.State().Get("clients", "carol"); !ok {
		t.Fatal("carol written on ru2 during the split did not reach fi1")
	}
}

func TestRestartContinuesSeqAndClock(t *testing.T) {
	dir := t.TempDir()
	r, _ := Open("ru2", dir, oplog.Options{})
	first, err := r.Set("clients", "a", map[string]any{"x": 1})
	must(t, first, err)
	r.Close()

	r = open(t, "ru2", dir)
	next, err := r.Set("clients", "a", map[string]any{"x": 2})
	must(t, next, err)
	if next.Seq != 2 || !next.Stamp.After(first.Stamp) {
		t.Fatalf("after restart seq=%d stamp %v !> %v", next.Seq, next.Stamp, first.Stamp)
	}
	if got, _ := r.State().Get("clients", "a"); string(got["x"]) != "2" {
		t.Fatalf("state after replay = %s", got)
	}
}

func TestIngestIsIdempotent(t *testing.T) {
	a, b := open(t, "fi1", t.TempDir()), open(t, "ru2", t.TempDir())
	o, err := a.Set("nodes", "nl2", map[string]any{"maintenance": true})
	must(t, o, err)
	ops, _ := a.Pull(oplog.Vector{}, 10)
	for range 3 {
		if _, err := b.Ingest(ops); err != nil {
			t.Fatal(err)
		}
	}
	if b.Vector()["fi1"] != 1 || a.State().Digest() != b.State().Digest() {
		t.Fatalf("re-delivery changed state: %v", b.Vector())
	}
}
