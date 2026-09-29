package oplog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
)

func op(origin string, seq uint64, key string) Op {
	return Op{
		Origin: origin, Seq: seq, Stamp: hlc.Stamp{Wall: int64(seq), Origin: origin},
		Table: "clients", Key: key, Fields: map[string]json.RawMessage{"remark": json.RawMessage(`"x"`)},
	}
}

func mustAppend(t *testing.T, l *Log, o Op) {
	t.Helper()
	if ok, err := l.Append(o); err != nil || !ok {
		t.Fatalf("append %s/%d: %v %v", o.Origin, o.Seq, ok, err)
	}
}

func TestAppendKeepsEachOriginInOrder(t *testing.T) {
	l, err := Open(t.TempDir(), Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	mustAppend(t, l, op("ru2", 1, "a"))
	mustAppend(t, l, op("ru2", 2, "b"))
	mustAppend(t, l, op("fi1", 1, "c"))
	if ok, err := l.Append(op("ru2", 2, "b")); ok || err != nil {
		t.Fatalf("re-delivery must be a no-op: %v %v", ok, err)
	}
	if _, err := l.Append(op("ru2", 4, "d")); !errors.Is(err, ErrGap) {
		t.Fatalf("gap accepted: %v", err)
	}
	if _, err := l.Append(op("../x", 1, "d")); err == nil {
		t.Fatal("path in origin accepted")
	}
	if v := l.Vector(); v["ru2"] != 2 || v["fi1"] != 1 || len(v) != 2 {
		t.Fatalf("vector %v", v)
	}
}

func TestSinceReturnsWhatThePeerMisses(t *testing.T) {
	l, _ := Open(t.TempDir(), Options{NoSync: true})
	defer l.Close()
	for i := uint64(1); i <= 3; i++ {
		mustAppend(t, l, op("ru2", i, "k"))
		mustAppend(t, l, op("de2", i, "k"))
	}
	got, err := l.Since(Vector{"ru2": 2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range got {
		ids = append(ids, o.Origin+string(rune('0'+o.Seq)))
	}
	if want := []string{"de21", "de22", "de23", "ru23"}; !equal(ids, want) {
		t.Fatalf("since = %v, want %v", ids, want)
	}
	if page, _ := l.Since(Vector{}, 2); len(page) != 2 {
		t.Fatalf("limit ignored: %d", len(page))
	}
}

func TestReopenCutsTornTail(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{})
	mustAppend(t, l, op("ru2", 1, "a"))
	mustAppend(t, l, op("ru2", 2, "b"))
	newest := l.Newest("ru2")
	l.Close()

	path := filepath.Join(dir, "ru2.log")
	full, _ := os.ReadFile(path)
	// a crash halfway through a third append
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.Write(full[:len(full)/3])
	f.Close()

	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if v := l.Vector(); v["ru2"] != 2 {
		t.Fatalf("after torn tail vector = %v", v)
	}
	if l.Newest("ru2") != newest {
		t.Fatalf("newest stamp lost: %v", l.Newest("ru2"))
	}
	mustAppend(t, l, op("ru2", 3, "c"))
	if got, _ := l.Since(Vector{"ru2": 2}, 10); len(got) != 1 || got[0].Key != "c" {
		t.Fatalf("append after repair: %+v", got)
	}
}

func TestReopenStopsAtCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{})
	mustAppend(t, l, op("ru2", 1, "a"))
	mustAppend(t, l, op("ru2", 2, "b"))
	l.Close()
	path := filepath.Join(dir, "ru2.log")
	b, _ := os.ReadFile(path)
	b[len(b)-2] ^= 0xff // flip a byte inside the second record
	os.WriteFile(path, b, 0o600)
	l, _ = Open(dir, Options{})
	defer l.Close()
	if v := l.Vector(); v["ru2"] != 1 {
		t.Fatalf("corrupt record kept: %v", v)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
