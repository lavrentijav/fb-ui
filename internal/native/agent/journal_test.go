package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

func add(t *testing.T, j *Journal, n int) {
	t.Helper()
	for range n {
		if _, err := j.Add(reports.Report{Online: []string{"alice"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func seqs(rs []reports.Report) []uint64 {
	var out []uint64
	for _, r := range rs {
		out = append(out, r.Seq)
	}
	return out
}

func reopen(t *testing.T, j *Journal, dir string) *Journal {
	t.Helper()
	j.Close()
	j, err := OpenJournal(dir, "nl2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func TestJournalKeepsUnackedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir, "nl2")
	if err != nil {
		t.Fatal(err)
	}
	add(t, j, 3)
	if err := j.Ack(2); err != nil {
		t.Fatal(err)
	}
	j = reopen(t, j, dir)
	if got := seqs(j.Pending(10)); len(got) != 1 || got[0] != 3 {
		t.Fatalf("pending after restart %v, want [3]", got)
	}
	if r, _ := j.Add(reports.Report{}); r.Seq != 4 || r.Node != "nl2" {
		t.Fatalf("next report %+v, want seq 4 of nl2", r)
	}
	if err := j.Ack(9); err == nil {
		t.Fatal("ack beyond the last report accepted")
	}
}

// Once all is acked the file is emptied, and numbering still continues
// after a restart: reused seqs would be dropped by masters as duplicates.
func TestJournalCompactsWithoutReusingSeqs(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir, "nl2")
	add(t, j, 5)
	if err := j.Ack(5); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "reports.log")); st.Size() != 0 {
		t.Fatalf("log not emptied: %d bytes", st.Size())
	}
	j = reopen(t, j, dir)
	if r, _ := j.Add(reports.Report{}); r.Seq != 6 {
		t.Fatalf("after compaction and restart seq %d, want 6", r.Seq)
	}
}

func TestJournalCutsTornTail(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir, "nl2")
	add(t, j, 2)
	j.Close()
	path := filepath.Join(dir, "reports.log")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, b[:len(b)/3]...), 0o600) // crash mid-append

	j, err := OpenJournal(dir, "nl2")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if got := seqs(j.Pending(10)); len(got) != 2 {
		t.Fatalf("pending %v, want [1 2]", got)
	}
	if r, _ := j.Add(reports.Report{}); r.Seq != 3 {
		t.Fatalf("seq after repair %d", r.Seq)
	}
}
