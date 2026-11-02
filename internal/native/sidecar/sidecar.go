// Package sidecar supervises the small proxy processes a node runs next to
// Xray (TUIC, MTProto, AmneziaWG). It follows the same rules as the core: a
// config change restarts only what changed, an unexpected exit is restarted
// with backoff, and removal stops the process before it disappears from the
// fleet.
//
// The supervisor knows nothing about a protocol: the master renders a spec per
// sidecar (binary, arguments, environment, config file content) and the node
// applies the set. Adding a protocol is a renderer, not another process manager.
package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Spec is one sidecar process and its configuration.
type Spec struct {
	// Name identifies the sidecar across updates; it is the map key.
	Name string `json:"name"`
	// Binary is the executable to run (absolute path on the node).
	Binary string `json:"binary"`
	// Args are the command-line arguments.
	Args []string `json:"args,omitempty"`
	// Env adds environment entries ("KEY=VALUE"); the process inherits the
	// node's environment too.
	Env []string `json:"env,omitempty"`
	// ConfigPath, when set, is where Config is written before start (atomically).
	ConfigPath string `json:"configPath,omitempty"`
	// Config is the file content for ConfigPath.
	Config []byte `json:"config,omitempty"`
	// Dir is the working directory (optional).
	Dir string `json:"dir,omitempty"`
}

// hash is what decides whether a running process must restart.
func (s Spec) hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", s.Name, s.Binary, s.Dir, strings.Join(s.Args, "\x00"))
	fmt.Fprintf(h, "%s\x00", strings.Join(s.Env, "\x00"))
	if s.ConfigPath != "" {
		fmt.Fprintf(h, "%s\x00", s.ConfigPath)
		h.Write(s.Config)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Validate refuses a spec the node could not run.
func (s Spec) Validate() error {
	if s.Name == "" {
		return errors.New("sidecar: a name is required")
	}
	if s.Binary == "" || !filepath.IsAbs(s.Binary) {
		return fmt.Errorf("sidecar %s: binary must be an absolute path", s.Name)
	}
	for _, e := range s.Env {
		if !strings.Contains(e, "=") {
			return fmt.Errorf("sidecar %s: env %q is not KEY=VALUE", s.Name, e)
		}
	}
	if len(s.Config) > 0 && s.ConfigPath == "" {
		return fmt.Errorf("sidecar %s: config content needs a configPath", s.Name)
	}
	return nil
}

// running is one supervised process.
type running struct {
	spec   Spec
	hash   string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
}

// Supervisor applies sidecar sets. The zero value is not usable; New is.
type Supervisor struct {
	// Logf receives process output and lifecycle lines.
	Logf func(format string, args ...any)
	// RestartDelay is the base backoff after an unexpected exit (default 2 s).
	RestartDelay time.Duration
	// StopTimeout is how long a removal waits between SIGTERM and kill
	// (default 5 s).
	StopTimeout time.Duration
	// Start, when set, replaces exec.CommandContext in tests.
	Start func(ctx context.Context, s Spec) *exec.Cmd

	mu      sync.Mutex
	procs   map[string]*running
	desired map[string]Spec
	ctx     context.Context
}

// New makes a supervisor bound to the node's lifetime.
func New(ctx context.Context, logf func(string, ...any)) *Supervisor {
	return &Supervisor{Logf: logf, procs: map[string]*running{}, desired: map[string]Spec{}, ctx: ctx}
}

// Apply makes the running set match specs: start new ones, restart changed
// ones, stop removed ones. It is safe to call repeatedly.
func (s *Supervisor) Apply(specs []Spec) error {
	next := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return err
		}
		if _, dup := next[spec.Name]; dup {
			return fmt.Errorf("sidecar: %q twice", spec.Name)
		}
		next[spec.Name] = spec
	}
	s.mu.Lock()
	s.desired = next
	var stop []*running
	start := map[string]Spec{}
	for name, cur := range s.procs {
		spec, want := next[name]
		if !want {
			stop = append(stop, cur)
			delete(s.procs, name)
			continue
		}
		if spec.hash() != cur.hash {
			stop = append(stop, cur)
			delete(s.procs, name)
			start[name] = spec
		}
	}
	for name, spec := range next {
		if _, up := s.procs[name]; !up {
			if _, scheduled := start[name]; !scheduled {
				start[name] = spec
			}
		}
	}
	s.mu.Unlock()

	for _, cur := range stop {
		s.stop(cur)
	}
	names := make([]string, 0, len(start))
	for name := range start {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := s.start(start[name]); err != nil {
			return err
		}
	}
	return nil
}

// start launches one spec and records it; the process is restarted on an
// unexpected exit while it remains desired.
func (s *Supervisor) start(spec Spec) error {
	if spec.ConfigPath != "" && len(spec.Config) > 0 {
		if err := writeAtomic(spec.ConfigPath, spec.Config); err != nil {
			return fmt.Errorf("sidecar %s: config: %w", spec.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	cmd := s.command(ctx, spec)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Dir = spec.Dir
	pr, pw, err := os.Pipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		cancel()
		return fmt.Errorf("sidecar %s: start: %w", spec.Name, err)
	}
	pw.Close()
	r := &running{spec: spec, hash: spec.hash(), cmd: cmd, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.procs[spec.Name] = r
	s.mu.Unlock()
	s.logf("sidecar %s started (pid %d)", spec.Name, cmd.Process.Pid)
	go s.pump(spec.Name, pr)
	go s.wait(ctx, r)
	return nil
}

// command builds the exec.Cmd, honouring the test seam.
func (s *Supervisor) command(ctx context.Context, spec Spec) *exec.Cmd {
	if s.Start != nil {
		return s.Start(ctx, spec)
	}
	return exec.CommandContext(ctx, spec.Binary, spec.Args...)
}

// pump copies the process output to the log.
func (s *Supervisor) pump(name string, r *os.File) {
	defer r.Close()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.logf("sidecar %s: %s", name, strings.TrimRight(string(buf[:n]), "\n"))
		}
		if err != nil {
			return
		}
	}
}

// wait owns the process' Wait call: it closes done, then restarts the spec if
// it is still desired and the process was not replaced or stopped.
func (s *Supervisor) wait(ctx context.Context, r *running) {
	err := r.cmd.Wait()
	close(r.done)
	s.mu.Lock()
	cur := s.procs[r.spec.Name]
	same := cur == r
	_, want := s.desired[r.spec.Name]
	s.mu.Unlock()
	if ctx.Err() != nil || !same || !want {
		return
	}
	s.logf("sidecar %s exited (%v); restarting", r.spec.Name, err)
	delay := s.RestartDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.mu.Lock()
	spec, stillWanted := s.desired[r.spec.Name]
	current := s.procs[r.spec.Name]
	s.mu.Unlock()
	if !stillWanted || current != r || spec.hash() != r.hash {
		return
	}
	s.mu.Lock()
	delete(s.procs, r.spec.Name) // let start install the new runner
	s.mu.Unlock()
	if err := s.start(spec); err != nil {
		s.logf("sidecar %s: restart: %v", spec.Name, err)
	}
}

// stop ends one process and waits for it.
func (s *Supervisor) stop(r *running) {
	r.cancel()
	timeout := s.StopTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-r.done:
	case <-time.After(timeout):
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
		<-r.done
	}
	s.logf("sidecar %s stopped", r.spec.Name)
}

// StopAll stops every process (node shutdown).
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	all := make([]*running, 0, len(s.procs))
	for _, r := range s.procs {
		all = append(all, r)
	}
	s.procs = map[string]*running{}
	s.desired = map[string]Spec{}
	s.mu.Unlock()
	for _, r := range all {
		s.stop(r)
	}
}

// Running answers the names currently supervised.
func (s *Supervisor) Running() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.procs))
	for name := range s.procs {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// writeAtomic writes a config file so a watcher never sees half of it.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Encode is the JSON a master stores per node in its desired state.
func Encode(specs []Spec) ([]byte, error) { return json.Marshal(specs) }
