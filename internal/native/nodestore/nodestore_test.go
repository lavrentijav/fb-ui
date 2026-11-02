package nodestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStateSurvivesReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node.db")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.State(ctx); !errors.Is(err, ErrNoState) {
		t.Fatalf("a fresh store answered %v", err)
	}
	want := State{Node: "ru4", Hash: "abc", Stamp: "1@ru2", Config: []byte(`{"x":1}`), At: time.UnixMilli(1700000000000)}
	if err := s.SaveState(ctx, want); err != nil {
		t.Fatal(err)
	}
	// a second save replaces the single row
	if err := s.SaveState(ctx, State{Node: "ru4", Hash: "def", Stamp: "2@ru2", Config: []byte(`{"x":2}`)}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != "ru4" || got.Hash != "def" || got.Stamp != "2@ru2" || string(got.Config) != `{"x":2}` || got.At.IsZero() {
		t.Fatalf("state %+v", got)
	}
}

func TestJournalNumbersNeverRepeatAcrossPrune(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	for i := 1; i <= 3; i++ {
		seq, err := s.AppendReport(ctx, now, []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		if seq != uint64(i) {
			t.Fatalf("seq %d, want %d", seq, i)
		}
	}
	pending, err := s.Pending(ctx, 10)
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending %v %v", pending, err)
	}
	if err := s.Ack(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingCount(ctx); err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}
	if _, err := s.Prune(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.PendingCount(ctx); n != 1 {
		t.Fatalf("an unacked report was pruned: %d", n)
	}
	seq, err := s.AppendReport(ctx, now, []byte{4})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("after prune seq %d, want 4", seq)
	}
	// pruning keeps the newest acknowledged rows for debugging
	if err := s.Ack(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows kept, want 1", rows)
	}
}

func TestTrafficAccumulatesAndResets(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, step := range []struct{ up, down int64 }{{10, 90}, {5, 5}} {
		if err := s.AddTraffic(ctx, "alice", step.up, step.down); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddTraffic(ctx, "bob", 1, 0); err != nil {
		t.Fatal(err)
	}
	one, ok, err := s.One(ctx, "alice")
	if err != nil || !ok || one.Up != 15 || one.Down != 95 {
		t.Fatalf("alice %+v %v %v", one, ok, err)
	}
	all, err := s.Traffic(ctx)
	if err != nil || len(all) != 2 || all["bob"].Up != 1 {
		t.Fatalf("all %v %v", all, err)
	}
	if err := s.ResetTraffic(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.One(ctx, "alice"); ok {
		t.Fatal("alice was not reset")
	}
}

func TestBansExpire(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.AddBan(ctx, Ban{IP: "10.0.0.1", Email: "alice", Reason: "ip limit", Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBan(ctx, Ban{IP: "10.0.0.1", Email: "alice", Reason: "ip limit", Until: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBan(ctx, Ban{IP: "10.0.0.2", Email: "bob", Reason: "fail2ban", Until: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	live, err := s.Bans(ctx, now)
	if err != nil || len(live) != 1 {
		t.Fatalf("live %v %v", live, err)
	}
	if live[0].Hits != 2 || live[0].Email != "alice" {
		t.Fatalf("ban %+v", live[0])
	}
	n, err := s.ExpireBans(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	if err := s.RemoveBan(ctx, "10.0.0.1", "alice"); err != nil {
		t.Fatal(err)
	}
	if live, _ := s.Bans(ctx, now); len(live) != 0 {
		t.Fatalf("left %v", live)
	}
}

func TestMetaAndSchemaVersion(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SetMeta(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "a", "2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetMeta(ctx, "a"); err != nil || !ok || v != "2" {
		t.Fatalf("%q %v %v", v, ok, err)
	}
	// a store from another schema is refused rather than half-read
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SetMeta(ctx, "schema", "999"); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("a foreign schema opened")
	}
}
