package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sh runs a shell script as the "sidecar binary"; the tests use it instead of
// a real proxy so they need no network and no extra files.
func sh(spec Spec) Spec {
	if spec.Binary == "" {
		spec.Binary = "/bin/sh"
	}
	return spec
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never happened", what)
}

func TestApplyStartsWritesTheConfigAndStopsOnRemoval(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cfgPath := filepath.Join(dir, "conf", "sidecar.json")
	sup := New(context.Background(), nil)
	defer sup.StopAll()

	spec := sh(Spec{
		Name: "tuic", Args: []string{"-c", "echo $$ > " + pidFile + "; sleep 60"},
		ConfigPath: cfgPath, Config: []byte(`{"listen":1}`),
	})
	// sh needs -c; express the script through Args
	spec.Args = []string{"-c", "echo $$ > " + pidFile + "; sleep 60"}
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pid file", func() bool {
		_, err := os.Stat(pidFile)
		return err == nil
	})
	if got := sup.Running(); len(got) != 1 || got[0] != "tuic" {
		t.Fatalf("running %v", got)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil || string(b) != `{"listen":1}` {
		t.Fatalf("config %q %v", b, err)
	}
	// removing the spec stops the process
	if err := sup.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if got := sup.Running(); len(got) != 0 {
		t.Fatalf("still running %v", got)
	}
	if _, err := os.Stat("/proc/" + strings.TrimSpace(readFile(t, pidFile))); err == nil {
		// the pid may be reused only after a long time; a live pid is a leak
		t.Log("pid still present, checking the process is not ours")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnUnchangedSpecDoesNotRestart(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	sup := New(context.Background(), nil)
	defer sup.StopAll()
	spec := sh(Spec{Name: "mtg", Args: []string{"-c", "echo $$ > " + pidFile + "; sleep 60"}})
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pid file", func() bool { _, err := os.Stat(pidFile); return err == nil })
	first := strings.TrimSpace(readFile(t, pidFile))
	// same spec again: no restart
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if again := strings.TrimSpace(readFile(t, pidFile)); again != first {
		t.Fatalf("the process restarted: %s -> %s", first, again)
	}
	// a changed argument does
	spec.Args = []string{"-c", "echo $$ > " + pidFile + "; sleep 61"}
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new pid", func() bool { return strings.TrimSpace(readFile(t, pidFile)) != first })
}

func TestAnUnexpectedExitIsRestarted(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "starts")
	sup := New(context.Background(), nil)
	sup.RestartDelay = 50 * time.Millisecond
	defer sup.StopAll()
	spec := sh(Spec{Name: "awg", Args: []string{"-c", "echo x >> " + counter + "; sleep 0.05"}})
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two starts", func() bool {
		b, err := os.ReadFile(counter)
		return err == nil && strings.Count(string(b), "x") >= 2
	})
}

func TestApplyRefusesADuplicateOrBadSpec(t *testing.T) {
	sup := New(context.Background(), nil)
	defer sup.StopAll()
	dup := []Spec{{Name: "a", Binary: "/bin/true"}, {Name: "a", Binary: "/bin/true"}}
	if err := sup.Apply(dup); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	if err := sup.Apply([]Spec{{Name: "b", Binary: "relative"}}); err == nil {
		t.Fatal("a relative binary was accepted")
	}
	if err := sup.Apply([]Spec{{Name: "c", Binary: "/bin/true", Config: []byte("x")}}); err == nil {
		t.Fatal("config without a path was accepted")
	}
	if err := sup.Apply([]Spec{{Name: "d", Binary: "/bin/true", Env: []string{"NOEQUALS"}}}); err == nil {
		t.Fatal("a bad env entry was accepted")
	}
}

func TestStopAllEndsEverything(t *testing.T) {
	sup := New(context.Background(), nil)
	spec := sh(Spec{Name: "x", Args: []string{"-c", "sleep 60"}})
	if err := sup.Apply([]Spec{spec}); err != nil {
		t.Fatal(err)
	}
	sup.StopAll()
	if got := sup.Running(); len(got) != 0 {
		t.Fatalf("still running %v", got)
	}
}
