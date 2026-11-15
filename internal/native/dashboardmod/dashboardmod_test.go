package dashboardmod

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

const uid = "11111111-2222-4333-8444-555555555555"

// source serves fixed reports over one stream.
type source struct{ reports []string }

func (s *source) ReportRange(context.Context, string) (uint64, uint64, error) {
	return 1, uint64(len(s.reports)), nil
}

func (s *source) GetReport(_ context.Context, _ string, seq uint64) ([]byte, error) {
	return []byte(s.reports[seq-1]), nil
}

func TestSummaryCounts(t *testing.T) {
	h := modtest.NewHost("m1")
	now := time.Unix(1_700_000_000, 0)
	h.Clock = func() time.Time { return now }
	ctx := context.Background()

	// two nodes with specs; one online with a user, one draining
	for _, node := range []string{"ru4", "de2"} {
		if _, err := h.Set(ctx, SpecTable, node, map[string]any{"spec": json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	src := &source{reports: []string{
		`{"n":"ru4","at":1700000000000,"o":["alice"]}`,
		`{"n":"de2","at":1700000000000,"dr":{"since":1699999000,"active":2},
		  "h":{"l1":0.5,"mu":100,"mt":400,"up":3600,"v":"dev"}}`,
	}}

	for _, c := range []clients.Client{
		{Node: "ru4", Inbound: "vpn", Email: "alice", UUID: uid, Enable: true},
		{Node: "ru4", Inbound: "vpn", Email: "bob", UUID: uid, Enable: false},
		{Node: "ru4", Inbound: "vpn", Email: "carol", UUID: uid, Enable: true, ExpiresAt: now.UnixMilli() - 1},
	} {
		if _, err := clients.Create(ctx, h, c); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = h.Set(ctx, usage.Table, "alice", map[string]any{"bytes": int64(1000)})
	_, _ = h.Set(ctx, usage.Table, "bob", map[string]any{"bytes": int64(500)})

	m := &Module{src: src, masters: []string{"ru4"}}
	s := m.summary(ctx, h)

	if s.Nodes.Total != 2 || s.Nodes.Online != 2 || s.Nodes.Draining != 1 || s.Nodes.Users != 1 {
		t.Fatalf("nodes %+v", s.Nodes)
	}
	if s.Clients.Total != 3 || s.Clients.Enabled != 2 || s.Clients.Disabled != 1 || s.Clients.Expired != 1 {
		t.Fatalf("clients %+v", s.Clients)
	}
	if s.Traffic.Bytes != 1500 {
		t.Fatalf("traffic %+v", s.Traffic)
	}
}

func TestSummaryWithoutAReportSource(t *testing.T) {
	h := modtest.NewHost("m1")
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	if _, err := h.Set(context.Background(), SpecTable, "ru4", map[string]any{"spec": json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	s := (&Module{}).summary(context.Background(), h)
	// a node with a spec but no report still counts, as offline
	if s.Nodes.Total != 1 || s.Nodes.Online != 0 {
		t.Fatalf("nodes %+v", s.Nodes)
	}
}
