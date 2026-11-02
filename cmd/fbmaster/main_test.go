package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir string, extra map[string]any) string {
	t.Helper()
	cfg := map[string]any{
		"name":     "fi1",
		"masters":  []string{"fi1"},
		"storeDir": filepath.Join(dir, "js"),
		"dataDir":  filepath.Join(dir, "data"),
	}
	for k, v := range extra {
		cfg[k] = v
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

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(io.Discard, "", 0)
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.EdgeListen != ":443" || c.ClientPort != 4222 || c.GatewayPort != 7222 {
		t.Fatalf("defaults %+v", c)
	}
	if c.PublicName != "fi1" {
		t.Fatalf("public name %q", c.PublicName)
	}
	if c.CACert != filepath.Join(dir, "data", "ca.pem") || c.Cert != filepath.Join(dir, "data", "master.pem") {
		t.Fatalf("identity paths %q %q", c.CACert, c.Cert)
	}
	if err := checkConfig(c); err != nil {
		t.Fatalf("check: %v", err)
	}

	// a peer that names this master, or a non-nats URL, is refused
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"peers": map[string]string{"fi1": "nats://x:7222"}})); err == nil {
		t.Fatal("a peer naming this master was accepted")
	}
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"peers": map[string]string{"ru2": "http://x:7222"}})); err == nil {
		t.Fatal("a non-nats peer URL was accepted")
	}
	// a missing name is refused
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"name": ""})); err == nil {
		t.Fatal("an empty name was accepted")
	}
}

func TestCheckRefusesAPublicAdminListener(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, map[string]any{"adminListen": "0.0.0.0:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(c); err == nil {
		t.Fatal("a public admin listener passed the check")
	}
	c.AdminAllowPublic = true
	if err := checkConfig(c); err != nil {
		t.Fatalf("explicit public listener: %v", err)
	}
}

func TestFirstRunCreatesTheCAAndIdentity(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, nil))
	if err != nil {
		t.Fatal(err)
	}
	logger := testLogger(t)
	// without -create-ca the master refuses to mint a CA
	if _, err := ensureCA(c, false, logger); err == nil {
		t.Fatal("a missing CA was accepted without -create-ca")
	}
	ca, err := ensureCA(c, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.CACert); err != nil {
		t.Fatalf("CA cert not written: %v", err)
	}
	if st, err := os.Stat(c.CAKey); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("CA key permissions: %v %v", st, err)
	}
	// a second run loads the same CA instead of minting another
	again, err := ensureCA(c, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	if again.Cert.SerialNumber.Cmp(ca.Cert.SerialNumber) != 0 {
		t.Fatal("the CA changed on the second run")
	}
	// and this master gets a certificate signed by it, written 0600
	id, err := ensureIdentity(c, ca, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Certificate) == 0 {
		t.Fatal("no certificate")
	}
	if st, err := os.Stat(c.Key); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("master key permissions: %v %v", st, err)
	}
	if _, err := ensureIdentity(c, ca, logger); err != nil {
		t.Fatalf("a second run did not reuse the identity: %v", err)
	}
}
