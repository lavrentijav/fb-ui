package nodestate_test

import (
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestate"
)

func TestAggregateMergesSpecsAndReports(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ms := now.UnixMilli()
	got := nodestate.Aggregate(
		[]string{"ru4", "de2", "nl2"},
		[]nodestate.Report{
			{Node: "ru4", At: ms - 5_000, Online: []string{"bob", "alice"}},
			{Node: "de2", At: ms - 120_000, Online: []string{"carol"}},
			{Node: "ru4", At: ms - 50_000, Online: []string{"stale"}}, // older: ignored
		},
		now, 60*time.Second,
	)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	by := map[string]nodestate.State{}
	for _, s := range got {
		by[s.Node] = s
	}
	if s := by["ru4"]; !s.Online || s.LastSeen != ms-5_000 || !slices.Equal(s.OnlineUsers, []string{"alice", "bob"}) || !s.HasSpec {
		t.Fatalf("ru4 %+v", s)
	}
	if s := by["de2"]; s.Online {
		t.Fatalf("an old report counts as online: %+v", s)
	}
	if s := by["nl2"]; !s.HasSpec || s.LastSeen != 0 || s.Online {
		t.Fatalf("a node with only a spec: %+v", s)
	}
}

func TestDrainAndNodesWithoutSpecs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := nodestate.Aggregate(nil, []nodestate.Report{
		{Node: "ru4", At: now.UnixMilli(), Online: []string{"alice"},
			Drain: &nodestate.Drain{Since: 1_699_999_000, Active: 3}},
	}, now, 0)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	s := got[0]
	if s.HasSpec || !s.Draining || s.DrainActive != 3 || s.DrainSince != 1_699_999_000 {
		t.Fatalf("%+v", s)
	}
}

func TestAggregateIsSortedAndDeduplicated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := nodestate.Aggregate([]string{"ru4", "de2", "ru4", ""}, nil, now, time.Minute)
	if len(got) != 2 || got[0].Node != "de2" || got[1].Node != "ru4" {
		t.Fatalf("%+v", got)
	}
}
