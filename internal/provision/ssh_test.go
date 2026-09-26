package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/provision/provisiontest"
)

func targetFor(s *provisiontest.Server) Target {
	return Target{Host: s.Host, Port: s.Port, User: "root"}
}

func TestDialPinsHostKeyAndRunsCommands(t *testing.T) {
	srv := provisiontest.Start(t, "hunter2", provisiontest.ShellHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tgt := targetFor(srv)
	tgt.Password = "wrong"
	if _, err := Dial(ctx, tgt); err == nil || !strings.Contains(err.Error(), "ssh handshake") {
		t.Fatalf("wrong password: err = %v", err)
	}

	tgt.Password = "hunter2"
	s, err := Dial(ctx, tgt)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if want := Fingerprint(srv.HostKey); s.HostKey != want {
		t.Fatalf("HostKey = %q, want %q", s.HostKey, want)
	}
	if s.UsedKey {
		t.Fatal("password login reported as key login")
	}

	out, err := s.Output(ctx, "echo hello; echo oops >&2")
	if err != nil || !strings.Contains(out, "hello") || !strings.Contains(out, "oops") {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if _, err := s.Output(ctx, "echo broke; exit 3"); err == nil || !strings.Contains(err.Error(), "exited 3: broke") {
		t.Fatalf("non-zero exit: err = %v", err)
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, "it's here.sh")
	if err := s.Upload(ctx, dst, 0o700, strings.NewReader("#!/bin/sh\necho uploaded\n")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("uploaded file: %v, mode %v", err, st)
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind after upload")
	}
	if out, _ := s.Output(ctx, ShellQuote(dst)); out != "uploaded" {
		t.Fatalf("running uploaded script: %q", out)
	}
	_ = s.Close()

	tgt.HostKeySHA256 = "SHA256:not-the-key"
	if _, err := Dial(ctx, tgt); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("pinned mismatch: err = %v, want ErrHostKeyMismatch", err)
	}
	tgt.HostKeySHA256 = Fingerprint(srv.HostKey)
	s, err = Dial(ctx, tgt)
	if err != nil {
		t.Fatalf("pinned match: %v", err)
	}
	_ = s.Close()

	keyOnly := targetFor(srv)
	keyOnly.PrivateKey = srv.UserKeyPEM(t)
	s, err = Dial(ctx, keyOnly)
	if err != nil || !s.UsedKey {
		t.Fatalf("key login: err=%v", err)
	}
	_ = s.Close()
}

func TestRunStopsOnContextCancel(t *testing.T) {
	srv := provisiontest.Start(t, "pw", provisiontest.ShellHandler)
	tgt := targetFor(srv)
	tgt.Password = "pw"
	s, err := Dial(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.Run(ctx, "sleep 30", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("Run on cancelled ctx: err=%v after %v", err, time.Since(start))
	}
}

func TestShellQuoteSurvivesTheShell(t *testing.T) {
	for _, in := range []string{"plain", "it's", `$(rm -rf /)`, "a b\tc", `"\`} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+ShellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Fatalf("ShellQuote(%q) round-trip = %q, %v", in, out, err)
		}
	}
}
