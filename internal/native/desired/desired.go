// Package desired moves a node's rendered config from any master to the
// node; the node applies a state only if it was rendered from newer data.
package desired

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

// State is what a node should run. Stamp is the master's projection version
// it was rendered from; Hash identifies Config.
type State struct {
	Node   string          `json:"node"`
	Stamp  hlc.Stamp       `json:"stamp"`
	Hash   string          `json:"hash"`
	Config json.RawMessage `json:"config"`
}

// Seal sets Hash from Config.
func (s *State) Seal() {
	sum := sha256.Sum256(s.Config)
	s.Hash = hex.EncodeToString(sum[:])
}

type request struct {
	Have hlc.Stamp `json:"have"`
}

type reply struct {
	Same  bool   `json:"same,omitempty"`
	State *State `json:"state,omitempty"`
	Err   string `json:"err,omitempty"`
}

// ErrNoState means a node has not applied a state yet.
var ErrNoState = errors.New("desired: no state yet")

// Render builds a node's state on this master.
type Render func(ctx context.Context, node string) (State, error)

// Serve answers nodes on desired.<master>.* over the master's connection.
func Serve(nc *nats.Conn, master string, render Render) (*nats.Subscription, error) {
	prefix := wire.DesiredSubject(master, "")
	return nc.Subscribe(wire.DesiredSubject(master, "*"), func(m *nats.Msg) {
		node := strings.TrimPrefix(m.Subject, prefix)
		var req request
		_ = json.Unmarshal(m.Data, &req)
		out := reply{}
		st, err := render(context.Background(), node)
		switch {
		case err != nil:
			out.Err = err.Error()
		case !st.Stamp.After(req.Have):
			out.Same = true
		default:
			st.Node = node
			st.Seal()
			out.State = &st
		}
		data, _ := json.Marshal(out)
		_ = m.Respond(data)
	})
}

// Backend is where the applied state is kept. A relay uses the file (nil
// backend); a full node keeps it in its micro SQLite.
type Backend struct {
	Load func() (State, error)
	Save func(State) error
}

// Applier keeps the node's applied state on disk and applies newer ones.
type Applier struct {
	path    string
	backend *Backend
	apply   func(ctx context.Context, st State) error

	mu  sync.Mutex
	cur State
}

// OpenApplier loads the last applied state from dir; apply puts a config
// into effect (write Xray config, reload) and must be safe to repeat.
func OpenApplier(dir string, apply func(context.Context, State) error) (*Applier, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a := &Applier{path: filepath.Join(dir, "desired.json"), apply: apply}
	b, err := os.ReadFile(a.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &a.cur); err != nil {
			return nil, fmt.Errorf("desired: %s: %w", a.path, err)
		}
	}
	return a, nil
}

// OpenApplierBackend loads the last applied state from backend; a Load that
// answers ErrNoState (wrapped or not) means a fresh node.
func OpenApplierBackend(backend Backend, apply func(context.Context, State) error) (*Applier, error) {
	a := &Applier{backend: &backend, apply: apply}
	st, err := backend.Load()
	switch {
	case errors.Is(err, ErrNoState), errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		a.cur = st
	}
	return a, nil
}

// Current is the applied state.
func (a *Applier) Current() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur
}

// Reapply puts the stored state into effect again, e.g. after a restart
// of the agent; with no state yet it does nothing.
func (a *Applier) Reapply(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur.Hash == "" {
		return nil
	}
	return a.apply(ctx, a.cur)
}

// Pull asks master for a newer state and applies it; a state with the
// same config only advances the stamp. It reports whether apply ran.
func (a *Applier) Pull(ctx context.Context, nc *nats.Conn, master, node string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, _ := json.Marshal(request{Have: a.cur.Stamp})
	msg, err := nc.RequestWithContext(ctx, wire.DesiredSubject(master, node), data)
	if err != nil {
		return false, err
	}
	var out reply
	if err := json.Unmarshal(msg.Data, &out); err != nil {
		return false, err
	}
	switch {
	case out.Err != "":
		return false, fmt.Errorf("desired: %s: %s", master, out.Err)
	case out.Same || out.State == nil || !out.State.Stamp.After(a.cur.Stamp):
		return false, nil
	}
	next := *out.State
	sum := sha256.Sum256(next.Config)
	if hex.EncodeToString(sum[:]) != next.Hash || next.Node != node {
		return false, errors.New("desired: state does not match its hash or node")
	}
	ran := next.Hash != a.cur.Hash
	if ran {
		if err := a.apply(ctx, next); err != nil {
			return false, err
		}
	}
	return ran, a.save(next)
}

func (a *Applier) save(st State) error {
	if a.backend != nil {
		if err := a.backend.Save(st); err != nil {
			return err
		}
		a.cur = st
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.cur = st
	return nil
}
