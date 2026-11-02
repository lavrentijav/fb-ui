package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setup(t *testing.T) (*Update, string) {
	t.Helper()
	dir := t.TempDir()
	cur := filepath.Join(dir, "fbnode")
	if err := os.WriteFile(cur, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &Update{
		Current: cur, MarkerPath: filepath.Join(dir, "upgrade.json"),
		MaxAttempts: 3, HealthyAfter: time.Minute,
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	return u, dir
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestApplySwapsAndKeepsThePreviousBinary(t *testing.T) {
	u, _ := setup(t)
	next := []byte("new-binary")
	if err := u.Apply(next, sum(next)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(u.Current)
	if err != nil || string(got) != "new-binary" {
		t.Fatalf("current = %q %v", got, err)
	}
	prev, err := os.ReadFile(u.previousPath())
	if err != nil || string(prev) != "old-binary" {
		t.Fatalf("previous = %q %v", prev, err)
	}
	if st, _ := os.Stat(u.Current); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	m, ok, err := u.Pending()
	if err != nil || !ok || m.Attempts != 0 || m.Previous == "" {
		t.Fatalf("marker %+v %v %v", m, ok, err)
	}
}

func TestApplyRefusesABadChecksumAndKeepsTheOldBinary(t *testing.T) {
	u, _ := setup(t)
	if err := u.Apply([]byte("new-binary"), sum([]byte("different"))); err != ErrBadChecksum {
		t.Fatalf("err = %v", err)
	}
	got, _ := os.ReadFile(u.Current)
	if string(got) != "old-binary" {
		t.Fatalf("the binary changed: %q", got)
	}
	if _, err := os.Stat(u.Current + ".new"); !os.IsNotExist(err) {
		t.Fatal("the rejected download was left behind")
	}
	if err := u.Apply([]byte("x"), ""); err == nil {
		t.Fatal("an empty checksum was accepted")
	}
}

func TestStartupCountsAttemptsThenRollsBack(t *testing.T) {
	u, _ := setup(t)
	next := []byte("new-binary")
	if err := u.Apply(next, sum(next)); err != nil {
		t.Fatal(err)
	}
	// up to three starts only count
	for want := 1; want <= 3; want++ {
		back, err := u.StartupCheck()
		if err != nil || back {
			t.Fatalf("start %d: back=%v err=%v", want, back, err)
		}
		if m, _, _ := u.Pending(); m.Attempts != want {
			t.Fatalf("attempts %d, want %d", m.Attempts, want)
		}
	}
	// the next start gives up and restores the previous binary
	back, err := u.StartupCheck()
	if err != nil || !back {
		t.Fatalf("rollback start: back=%v err=%v", back, err)
	}
	got, _ := os.ReadFile(u.Current)
	if string(got) != "old-binary" {
		t.Fatalf("rolled back to %q", got)
	}
	if _, ok, _ := u.Pending(); ok {
		t.Fatal("the marker survived the rollback")
	}
}

func TestHealthyClearsTheMarker(t *testing.T) {
	u, _ := setup(t)
	next := []byte("new-binary")
	if err := u.Apply(next, sum(next)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.StartupCheck(); err != nil {
		t.Fatal(err)
	}
	if err := u.Healthy(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := u.Pending(); ok {
		t.Fatal("the marker outlived a healthy start")
	}
	// a later start is a normal start
	back, err := u.StartupCheck()
	if err != nil || back {
		t.Fatalf("back=%v err=%v", back, err)
	}
}

func TestARollbackWithoutAPreviousBinaryDropsTheMarker(t *testing.T) {
	u, _ := setup(t)
	if err := u.writeMarker(Marker{Current: u.Current, Attempts: 99, Since: u.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	back, err := u.StartupCheck()
	if err != nil || !back {
		t.Fatalf("back=%v err=%v", back, err)
	}
	if _, ok, _ := u.Pending(); ok {
		t.Fatal("a broken marker was kept")
	}
}
