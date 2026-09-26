// Package provision drives a fresh server over SSH: connect with a password or
// key, pin the host key, upload files and run commands with streamed output.
// It is a leaf package; the node-provisioning service decides what to run.
package provision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target is how to reach the server. Secrets live only in this struct for the
// duration of a job; nothing here is persisted.
type Target struct {
	Host       string
	Port       int
	User       string
	Password   string
	PrivateKey string
	// HostKeySHA256 pins the server key ("SHA256:..." as ssh-keygen prints it);
	// empty trusts the first key seen and reports it back.
	HostKeySHA256 string
}

// Session is one authenticated SSH connection.
type Session struct {
	client  *ssh.Client
	HostKey string
	UsedKey bool
}

var ErrHostKeyMismatch = errors.New("provision: host key does not match the pinned fingerprint")

// Fingerprint formats a host key the way ssh-keygen -lf does.
func Fingerprint(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Dial connects and authenticates. A key is tried before the password so a
// server that already disabled password login still works.
func Dial(ctx context.Context, t Target) (*Session, error) {
	if t.Port == 0 {
		t.Port = 22
	}
	if t.User == "" {
		t.User = "root"
	}
	var auths []ssh.AuthMethod
	usedKey := false
	if strings.TrimSpace(t.PrivateKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(t.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("provision: private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
		usedKey = true
	}
	if t.Password != "" {
		pw := t.Password
		auths = append(auths, ssh.Password(pw), ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}))
	}
	if len(auths) == 0 {
		return nil, errors.New("provision: a password or a private key is required")
	}

	if strings.TrimSpace(t.HostKeySHA256) == "" {
		return handshake(ctx, t, auths, usedKey, hostKeyPreference)
	}
	// A pinned fingerprint names one key type, but a server offers several
	// and the client picks; try each type until the pinned one is presented.
	var lastErr error
	for _, algo := range hostKeyPreference {
		s, err := handshake(ctx, t, auths, usedKey, []string{algo})
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, ErrHostKeyMismatch) && !strings.Contains(err.Error(), "no common algorithm") {
			return nil, err
		}
		if lastErr == nil || errors.Is(err, ErrHostKeyMismatch) {
			lastErr = err
		}
	}
	return nil, lastErr
}

// hostKeyPreference puts ed25519 first: it is the key operators copy from
// ssh-keyscan or a provider console, so the reported fingerprint matches.
var hostKeyPreference = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

func handshake(ctx context.Context, t Target, auths []ssh.AuthMethod, usedKey bool, hostKeyAlgos []string) (*Session, error) {
	var seen string
	cfg := &ssh.ClientConfig{
		User:              t.User,
		Auth:              auths,
		HostKeyAlgorithms: hostKeyAlgos,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = Fingerprint(key)
			if t.HostKeySHA256 != "" && seen != strings.TrimSpace(t.HostKeySHA256) {
				return fmt.Errorf("%w: server presented %s", ErrHostKeyMismatch, seen)
			}
			return nil
		},
		Timeout: 20 * time.Second,
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("provision: connect %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("provision: ssh handshake with %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Time{})
	s := &Session{client: ssh.NewClient(c, chans, reqs), HostKey: seen}
	// Report key auth only when the password was not also offered: with both,
	// we cannot tell which one the server accepted.
	s.UsedKey = usedKey && t.Password == ""
	return s, nil
}

func (s *Session) Close() error { return s.client.Close() }

// Run executes cmd, feeding stdin when non-nil and calling line for every
// output line (stdout and stderr interleaved). It returns the exit status.
func (s *Session) Run(ctx context.Context, cmd string, stdin io.Reader, line func(string)) (int, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("provision: new session: %w", err)
	}
	defer sess.Close()
	if stdin != nil {
		sess.Stdin = stdin
	}
	pr, pw := io.Pipe()
	sess.Stdout, sess.Stderr = pw, pw
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if line != nil {
				line(sc.Text())
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()

	if err := sess.Start(cmd); err != nil {
		_ = pw.Close()
		wg.Wait()
		return -1, fmt.Errorf("provision: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		_ = sess.Close()
		waitErr = ctx.Err()
	}
	_ = pw.Close()
	wg.Wait()

	var exitErr *ssh.ExitError
	switch {
	case waitErr == nil:
		return 0, nil
	case errors.As(waitErr, &exitErr):
		return exitErr.ExitStatus(), nil
	default:
		return -1, waitErr
	}
}

// Output runs cmd and returns its trimmed combined output, failing on a
// non-zero exit.
func (s *Session) Output(ctx context.Context, cmd string) (string, error) {
	var b strings.Builder
	code, err := s.Run(ctx, cmd, nil, func(l string) {
		b.WriteString(l)
		b.WriteByte('\n')
	})
	out := strings.TrimSpace(b.String())
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("provision: %q exited %d: %s", firstWord(cmd), code, lastLine(out))
	}
	return out, nil
}

// Upload writes r to path with the given octal mode, via a temp file and
// rename so an interrupted upload never leaves a half-written file behind.
func (s *Session) Upload(ctx context.Context, path string, mode uint32, r io.Reader) error {
	q := ShellQuote(path)
	tmp := ShellQuote(path + ".part")
	cmd := fmt.Sprintf("umask 077 && cat > %s && chmod %o %s && mv -f %s %s", tmp, mode, tmp, tmp, q)
	var tail string
	code, err := s.Run(ctx, cmd, r, func(l string) { tail = l })
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("provision: upload %s exited %d: %s", path, code, tail)
	}
	return nil
}

// ShellQuote single-quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
