package usagemod

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// source fakes one master's report stream.
type source struct {
	reports []string
}

func (s *source) ReportRange(context.Context, string) (uint64, uint64, error) {
	return 1, uint64(len(s.reports)), nil
}

func (s *source) GetReport(_ context.Context, _ string, seq uint64) ([]byte, error) {
	return []byte(s.reports[seq-1]), nil
}

func report(entries map[string][2]int64) string {
	var traffic []map[string]any
	for email, v := range entries {
		traffic = append(traffic, map[string]any{"e": email, "u": v[0], "d": v[1]})
	}
	raw, _ := json.Marshal(map[string]any{"n": "ru4", "at": 1, "t": traffic})
	return string(raw)
}

const uid = "11111111-2222-4333-8444-555555555555"

func TestCountsEachReportOnceAndSnapshots(t *testing.T) {
	h := modtest.NewHost("m1")
	h.IsLeader = true
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	src := &source{reports: []string{
		report(map[string][2]int64{"alice": {10, 20}}),
		report(map[string][2]int64{"alice": {1, 2}, "bob": {5, 5}}),
	}}
	m := &Module{Source: src, Masters: []string{"ru4"}, Tracker: usage.New()}
	ctx := context.Background()
	if err := m.tick(ctx, h); err != nil {
		t.Fatal(err)
	}
	if got := m.Tracker.Total("alice"); got != 33 {
		t.Fatalf("alice %d", got)
	}
	if got := m.Tracker.Total("bob"); got != 10 {
		t.Fatalf("bob %d", got)
	}
	// a snapshot is in the op log
	if _, ok := h.Get(usage.Table, "alice"); !ok {
		t.Fatal("no snapshot")
	}

	// a second tick reads nothing new: the cursor moved
	src.reports = append(src.reports, report(map[string][2]int64{"alice": {100, 0}}))
	if err := m.tick(ctx, h); err != nil {
		t.Fatal(err)
	}
	if got := m.Tracker.Total("alice"); got != 133 {
		t.Fatalf("alice after a new report: %d", got)
	}
}

func TestEnforcesTheAllowanceOnce(t *testing.T) {
	h := modtest.NewHost("m1")
	h.IsLeader = true
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	ctx := context.Background()
	c := clients.Client{Node: "ru4", Inbound: "vpn", Email: "alice", UUID: uid, Enable: true, TotalGB: 50}
	if _, err := clients.Create(ctx, h, c); err != nil {
		t.Fatal(err)
	}
	unlimited := c
	unlimited.Email, unlimited.TotalGB = "bob", 0
	if _, err := clients.Create(ctx, h, unlimited); err != nil {
		t.Fatal(err)
	}

	src := &source{reports: []string{report(map[string][2]int64{"alice": {30, 25}, "bob": {1 << 40, 0}})}}
	m := &Module{Source: src, Masters: []string{"ru4"}, Tracker: usage.New()}
	if err := m.tick(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, _ := clients.Get(h, "ru4", "vpn", "alice")
	if got.Enable {
		t.Fatal("an over-quota client stayed enabled")
	}
	if b, _ := clients.Get(h, "ru4", "vpn", "bob"); !b.Enable {
		t.Fatal("an unlimited client was disabled")
	}
	if len(h.Keys(module.EventTable)) != 1 {
		t.Fatalf("events %v", h.Keys(module.EventTable))
	}

	// the sweep is idempotent: no second event, no new write
	if err := m.tick(ctx, h); err != nil {
		t.Fatal(err)
	}
	if len(h.Keys(module.EventTable)) != 1 {
		t.Fatalf("a second event was emitted: %v", h.Keys(module.EventTable))
	}
}

func TestResetClearsTheCount(t *testing.T) {
	h := modtest.NewHost("m1")
	m := &Module{Tracker: usage.New()}
	m.Tracker.Add("alice", "ru4", 100, 100)
	if err := m.Tracker.Reset(context.Background(), h, "alice"); err != nil {
		t.Fatal(err)
	}
	if m.Tracker.Total("alice") != 0 {
		t.Fatal("not reset")
	}
	// a fresh tracker loads the zero snapshot, not the old total
	again := usage.New()
	again.Load(h)
	if again.Total("alice") != 0 {
		t.Fatalf("a restart restored the old count: %d", again.Total("alice"))
	}
}
