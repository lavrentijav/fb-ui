package lww

import (
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

func set(origin string, wall int64, key string, kv ...string) oplog.Op {
	f := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		f[kv[i]] = json.RawMessage(kv[i+1])
	}
	return oplog.Op{Origin: origin, Stamp: hlc.Stamp{Wall: wall, Origin: origin}, Table: "clients", Key: key, Fields: f}
}

func del(origin string, wall int64, key string) oplog.Op {
	return oplog.Op{Origin: origin, Stamp: hlc.Stamp{Wall: wall, Origin: origin}, Table: "clients", Key: key, Delete: true}
}

func TestConcurrentEditsOfDifferentFieldsBothSurvive(t *testing.T) {
	s := New()
	s.Apply(set("ru2", 10, "alice", "remark", `"old"`, "limitGB", `50`))
	s.Apply(set("fi1", 20, "alice", "remark", `"from fi1"`)) // fi1 renames
	s.Apply(set("ru2", 15, "alice", "limitGB", `100`))       // ru2 raises limit meanwhile
	got, ok := s.Get("clients", "alice")
	if !ok || string(got["remark"]) != `"from fi1"` || string(got["limitGB"]) != `100` {
		t.Fatalf("merged = %s", got)
	}
}

func TestDeleteHidesOlderAndNewerWriteRevives(t *testing.T) {
	s := New()
	s.Apply(set("ru2", 10, "bob", "remark", `"b"`, "limitGB", `5`))
	s.Apply(del("de2", 20, "bob"))
	if _, ok := s.Get("clients", "bob"); ok || len(s.Keys("clients")) != 0 {
		t.Fatal("deleted record still visible")
	}
	s.Apply(set("fi1", 30, "bob", "remark", `"back"`))
	got, ok := s.Get("clients", "bob")
	if !ok || len(got) != 1 || string(got["remark"]) != `"back"` {
		t.Fatalf("revived = %s %v", got, ok)
	}
}

func TestSameStampTieBrokenByOrigin(t *testing.T) {
	a, b := New(), New()
	x, y := set("de2", 10, "k", "v", `1`), set("ru2", 10, "k", "v", `2`)
	a.Apply(x)
	a.Apply(y)
	b.Apply(y)
	b.Apply(x)
	if a.Digest() != b.Digest() {
		t.Fatal("equal wall times diverged")
	}
	if got, _ := a.Get("clients", "k"); string(got["v"]) != `2` {
		t.Fatalf("winner = %s, want ru2's", got["v"])
	}
}

// The property the whole design rests on: any delivery order, with
// duplicates, converges to one state.
func TestAnyOrderConverges(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	origins := []string{"fi1", "ru2", "de2"}
	keys := []string{"a", "b", "c"}
	names := []string{"remark", "limitGB", "enable"}
	var ops []oplog.Op
	for i := range 300 {
		o, k := origins[r.IntN(3)], keys[r.IntN(3)]
		wall := int64(r.IntN(50)) // many equal walls on purpose
		if r.IntN(6) == 0 {
			ops = append(ops, del(o, wall, k))
			continue
		}
		ops = append(ops, set(o, wall, k, names[r.IntN(3)], string(rune('0'+i%10))))
	}
	want := New()
	for _, op := range ops {
		want.Apply(op)
	}
	for trial := range 50 {
		shuffled := append([]oplog.Op(nil), ops...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		shuffled = append(shuffled, shuffled[:len(shuffled)/4]...) // re-deliveries
		got := New()
		for _, op := range shuffled {
			got.Apply(op)
		}
		if got.Digest() != want.Digest() {
			t.Fatalf("trial %d diverged", trial)
		}
	}
}
