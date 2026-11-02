// Package selfupdate replaces the node's own binary with a verified download
// and rolls back when the new one does not stay up.
//
// The node runs under a supervisor (systemd Restart=always), so the flow is:
//
//  1. download to <current>.new, verify the pinned sha256, chmod 0755;
//  2. keep the running binary as <current>.previous, move the new one into place;
//  3. write a marker; exit so the supervisor starts the new binary;
//  4. the new process counts its starts in the marker; once it has been healthy
//     long enough the marker is cleared;
//  5. if it keeps dying, the next start restores <previous> and exits, and the
//     supervisor brings the old binary back.
//
// A binary cannot be replaced byte-by-byte while it runs; rename(2) over it is
// the Linux rule the swap relies on.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrBadChecksum is a download that does not match the pinned digest.
var ErrBadChecksum = errors.New("selfupdate: checksum mismatch")

// Marker records an update in progress.
type Marker struct {
	Previous string `json:"previous"`
	Current  string `json:"current"`
	Attempts int    `json:"attempts"`
	Since    int64  `json:"since"`
}

// Update is one node's updater.
type Update struct {
	// Current is the path of the running binary.
	Current string
	// MarkerPath is where the in-progress record lives.
	MarkerPath string
	// MaxAttempts is how many starts the new binary gets before rollback
	// (default 3).
	MaxAttempts int
	// HealthyAfter is how long a start must stay up before the update counts
	// as good (default 60 s).
	HealthyAfter time.Duration
	Now          func() time.Time
}

func (u *Update) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Update) maxAttempts() int {
	if u.MaxAttempts > 0 {
		return u.MaxAttempts
	}
	return 3
}

func (u *Update) healthyAfter() time.Duration {
	if u.HealthyAfter > 0 {
		return u.HealthyAfter
	}
	return 60 * time.Second
}

// previousPath is where the binary being replaced is kept.
func (u *Update) previousPath() string { return u.Current + ".previous" }

// Apply installs data as the current binary after checking it against sum (the
// hex sha256 from the release manifest).
func (u *Update) Apply(data []byte, sum string) error {
	want := strings.ToLower(strings.TrimSpace(sum))
	got := sha256.Sum256(data)
	if want == "" {
		return errors.New("selfupdate: a sha256 is required")
	}
	if hex.EncodeToString(got[:]) != want {
		return ErrBadChecksum
	}
	dir := filepath.Dir(u.Current)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := u.Current + ".new"
	if err := writeSynced(tmp, data, 0o755); err != nil {
		return err
	}
	// keep the running binary, then take its name: on Linux a running
	// executable may be replaced by rename
	if _, err := os.Stat(u.Current); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("selfupdate: current binary: %w", err)
	}
	if err := os.Rename(u.Current, u.previousPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, u.Current); err != nil {
		_ = os.Rename(u.previousPath(), u.Current) // put the old one back
		return err
	}
	return u.writeMarker(Marker{
		Previous: u.previousPath(), Current: u.Current, Attempts: 0, Since: u.now().Unix(),
	})
}

// StartupCheck runs at process start. It answers rolledBack=true when the
// previous binary was restored: the process must exit so the supervisor starts
// it, rather than keep running as the failed build.
func (u *Update) StartupCheck() (rolledBack bool, err error) {
	m, ok, err := u.readMarker()
	if err != nil || !ok {
		return false, err
	}
	if m.Attempts >= u.maxAttempts() {
		if err := u.rollback(); err != nil {
			return false, err
		}
		return true, nil
	}
	m.Attempts++
	if m.Since == 0 {
		m.Since = u.now().Unix()
	}
	return false, u.writeMarker(m)
}

// rollback puts the previous binary back and clears the marker.
func (u *Update) rollback() error {
	if _, err := os.Stat(u.previousPath()); err != nil {
		// nothing to restore: drop the marker so a broken update cannot loop
		return u.Clear()
	}
	if err := os.Rename(u.previousPath(), u.Current); err != nil {
		return err
	}
	return u.Clear()
}

// Healthy clears the marker once the new binary has proved itself.
func (u *Update) Healthy() error { return u.Clear() }

// Pending answers the update record still in force, if any.
func (u *Update) Pending() (Marker, bool, error) { return u.readMarker() }

// Clear removes the marker.
func (u *Update) Clear() error {
	err := os.Remove(u.MarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// HealthyAfter exposes the wait a caller should use before Healthy.
func (u *Update) HealthyAfterWait() time.Duration { return u.healthyAfter() }

func (u *Update) readMarker() (Marker, bool, error) {
	b, err := os.ReadFile(u.MarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, false, fmt.Errorf("selfupdate: marker: %w", err)
	}
	return m, true, nil
}

func (u *Update) writeMarker(m Marker) error {
	if err := os.MkdirAll(filepath.Dir(u.MarkerPath), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeSynced(u.MarkerPath, b, 0o600)
}

// writeSynced writes a file and fsyncs it, so a crash cannot leave half a
// binary or half a marker.
func writeSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
