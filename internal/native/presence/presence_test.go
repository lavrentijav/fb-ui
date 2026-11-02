package presence_test

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

func TestWhereNewestWinsAndEntriesExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	m := &presence.Map{TTL: 10 * time.Second, Now: func() time.Time { return now }}
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice", "bob"}, At: now.UnixMilli()})
	m.Add(presence.Report{Node: "de2", Users: []string{"alice"}, At: now.Add(time.Second).UnixMilli()})
	w := m.Where()
	if w["alice"] != "de2" || w["bob"] != "ru4" {
		t.Fatalf("where = %v", w)
	}
	select {
	case <-m.Changed():
	default:
		t.Fatal("no change signalled")
	}
	// ru4 keeps reporting alice, but de2's newer report still wins
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice", "bob"}, At: now.Add(500 * time.Millisecond).UnixMilli()})
	if m.Where()["alice"] != "de2" {
		t.Fatal("an older report took alice back")
	}
	// review focus 2: de2 dies silently; after TTL alice falls back to ru4's report
	now = now.Add(11 * time.Second)
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice"}, At: now.UnixMilli()})
	if w := m.Where(); w["alice"] != "ru4" || w["bob"] != "" {
		t.Fatalf("after de2 went quiet: %v", w)
	}
}
