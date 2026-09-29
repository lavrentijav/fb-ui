package hlc

import "testing"

func TestNowIsMonotonicWhenWallStepsBack(t *testing.T) {
	wall := int64(1000)
	c := NewWithTime("ru2", func() int64 { return wall })
	a := c.Now()
	wall = 900 // NTP stepped the clock back
	b := c.Now()
	wall = 1000
	d := c.Now()
	if !b.After(a) || !d.After(b) {
		t.Fatalf("not monotonic: %v %v %v", a, b, d)
	}
	wall = 2000
	if e := c.Now(); e != (Stamp{Wall: 2000, Origin: "ru2"}) {
		t.Fatalf("wall ahead again should reset logical: %v", e)
	}
}

func TestObserveOrdersLocalAfterRemote(t *testing.T) {
	c := NewWithTime("ru2", func() int64 { return 1000 })
	remote := Stamp{Wall: 5000, Logical: 7, Origin: "fi1"}
	c.Observe(remote)
	if s := c.Now(); !s.After(remote) || s.Origin != "ru2" {
		t.Fatalf("local %v not after remote %v", s, remote)
	}
}

func TestTieBrokenByOrigin(t *testing.T) {
	a := Stamp{Wall: 1, Logical: 0, Origin: "de2"}
	b := Stamp{Wall: 1, Logical: 0, Origin: "fi1"}
	if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(a) != 0 {
		t.Fatal("origin must break ties the same way everywhere")
	}
	if !(Stamp{}).IsZero() || !a.After(Stamp{}) {
		t.Fatal("zero stamp must be the oldest")
	}
}

func TestStringRoundTrip(t *testing.T) {
	s := Stamp{Wall: 1790629283123, Logical: 42, Origin: "ru2"}
	got, err := Parse(s.String())
	if err != nil || got != s {
		t.Fatalf("round trip: %v %v", got, err)
	}
	for _, bad := range []string{"", "1.2", "x.1@a", "1.x@a", "1@a"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted", bad)
		}
	}
}
