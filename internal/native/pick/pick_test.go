package pick

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu sync.Mutex
	m  map[string]Measure
}

func (f *fake) set(name string, m Measure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[name] = m
}

func (f *fake) down(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, name)
}

func (f *fake) probe(_ context.Context, name string) (Measure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.m[name]; ok {
		return m, nil
	}
	return Measure{}, errors.New("unreachable")
}

func rtt(ms int) Measure {
	return Measure{RTT: time.Duration(ms) * time.Millisecond, Throughput: 50e6, MaxPacket: 1448}
}

func setup(t *testing.T) (*Selector, *fake, *time.Time) {
	t.Helper()
	f := &fake{m: map[string]Measure{"fi1": rtt(40), "ru2": rtt(8), "de2": rtt(35)}}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s := New([]string{"fi1", "ru2", "de2"}, f.probe, Settings{RebindEvery: time.Hour, MinPacket: 1200})
	s.now = func() time.Time { return now }
	return s, f, &now
}

func tick(t *testing.T, s *Selector) string {
	t.Helper()
	got, err := s.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPicksNearestAndKeepsItUntilRebind(t *testing.T) {
	s, f, now := setup(t)
	if got := tick(t, s); got != "ru2" {
		t.Fatalf("first pick %s, want nearest ru2", got)
	}
	f.set("fi1", rtt(2)) // fi1 became much closer
	*now = now.Add(30 * time.Minute)
	if got := tick(t, s); got != "ru2" {
		t.Fatalf("rebound after 30m to %s; rebind is hourly", got)
	}
	*now = now.Add(31 * time.Minute)
	if got := tick(t, s); got != "fi1" {
		t.Fatalf("after an hour got %s, want fi1", got)
	}
}

func TestHysteresisAvoidsFlapping(t *testing.T) {
	s, f, now := setup(t)
	tick(t, s)
	f.set("de2", rtt(7)) // barely better than ru2's 8 ms
	*now = now.Add(time.Hour)
	if got := tick(t, s); got != "ru2" {
		t.Fatalf("switched to %s for a 1 ms gain", got)
	}
}

func TestLoadSmallPacketsAndSlowLinksCount(t *testing.T) {
	cases := []struct {
		name string
		ru2  Measure
	}{
		{"loaded master", Measure{RTT: 8 * time.Millisecond, Throughput: 50e6, MaxPacket: 1448, Load: 5}},
		{"fragmenting path", Measure{RTT: 8 * time.Millisecond, Throughput: 50e6, MaxPacket: 536}},
		{"slow link", Measure{RTT: 8 * time.Millisecond, Throughput: 20e3, MaxPacket: 1448}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, f, _ := setup(t)
			f.set("ru2", c.ru2)
			if got := tick(t, s); got != "de2" {
				t.Fatalf("got %s, want de2 (scores ru2=%.0f de2=%.0f)", got, s.set.Score(c.ru2), s.set.Score(rtt(35)))
			}
		})
	}
}

func TestFailureRebindsAtOnce(t *testing.T) {
	s, f, now := setup(t)
	tick(t, s)
	f.down("ru2")
	s.Failed("ru2")
	*now = now.Add(time.Minute)
	if got := tick(t, s); got != "de2" {
		t.Fatalf("after ru2 failed got %s, want de2 without waiting an hour", got)
	}
	f.down("fi1")
	f.down("de2")
	s.Failed("de2")
	if got, err := s.Tick(context.Background()); !errors.Is(err, ErrNoMaster) || got != "de2" {
		t.Fatalf("all down: %s %v; keep last binding and report", got, err)
	}
	f.set("fi1", rtt(40))
	if got := tick(t, s); got != "fi1" {
		t.Fatalf("recovery picked %s", got)
	}
}
