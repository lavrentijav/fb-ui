package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeNodeConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := map[string]any{
		"node": "ru4", "profile": "lite", "masters": map[string]string{"fi1": "tls://fi1:443"},
		"dataDir": filepath.Join(dir, "data"), "xray": map[string]string{"bin": "/bin/true"},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The upgrade command downloads, verifies and swaps the binary, writing the
// rollback marker next to the config's dataDir. It does not reach for the
// running executable in this test: -current points at a stand-in.
func TestUpgradeInstallsAVerifiedBinary(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "fbnode")
	if err := os.WriteFile(cur, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := []byte("new-binary")
	sum := sha256.Sum256(next)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(next)
	}))
	defer srv.Close()

	cfgPath := writeNodeConfig(t, dir)
	if err := runUpgrade([]string{"-c", cfgPath, "-url", srv.URL, "-sha256", hex.EncodeToString(sum[:]), "-current", cur}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cur)
	if string(got) != "new-binary" {
		t.Fatalf("installed %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "upgrade.json")); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if prev, _ := os.ReadFile(cur + ".previous"); string(prev) != "old" {
		t.Fatalf("previous %q", prev)
	}
}

func TestUpgradeRefusesABadChecksum(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "fbnode")
	if err := os.WriteFile(cur, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	cfgPath := writeNodeConfig(t, dir)
	err := runUpgrade([]string{"-c", cfgPath, "-url", srv.URL, "-sha256", "00", "-current", cur}, io.Discard)
	if err == nil {
		t.Fatal("a bad checksum was accepted")
	}
	if got, _ := os.ReadFile(cur); string(got) != "old" {
		t.Fatalf("the binary changed: %q", got)
	}
}
