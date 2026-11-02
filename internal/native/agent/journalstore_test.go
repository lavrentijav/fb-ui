package agent_test

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestore"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

func TestStoreJournalNumbersAndAcks(t *testing.T) {
	store, err := nodestore.Open(filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	j := agent.OpenStoreJournal(store, "ru4")

	first, err := j.Add(reports.Report{At: 1000, Traffic: []reports.Traffic{{Email: "alice", Up: 1, Down: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Add(reports.Report{At: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || second.Seq != 2 || first.Node != "ru4" {
		t.Fatalf("seq %d %d node %q", first.Seq, second.Seq, first.Node)
	}
	pending := j.Pending(10)
	if len(pending) != 2 || pending[0].Seq != 1 || len(pending[0].Traffic) != 1 || pending[0].Traffic[0].Email != "alice" {
		t.Fatalf("pending %+v", pending)
	}
	if err := j.Ack(1); err != nil {
		t.Fatal(err)
	}
	pending = j.Pending(10)
	if len(pending) != 1 || pending[0].Seq != 2 {
		t.Fatalf("after ack %+v", pending)
	}
	// a reopened journal continues the numbering
	again := agent.OpenStoreJournal(store, "ru4")
	third, err := again.Add(reports.Report{At: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if third.Seq != 3 {
		t.Fatalf("after reopen seq %d, want 3", third.Seq)
	}
}
