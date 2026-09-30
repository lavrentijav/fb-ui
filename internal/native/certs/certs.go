// Package certs keeps the fleet's certificates on the masters: any master
// may renew one, a lease written through the op log makes one of them do it.
package certs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const (
	Table      = "certs"
	LeaseTable = "certlease"
)

// Reader is the projected state (master.Mem's State, later pgstate).
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

// Issued is what an ACME order returned.
type Issued struct {
	CertPEM, KeyPEM string
	NotAfter        time.Time
}

type Issuer func(ctx context.Context, names []string) (Issued, error)

type Renewer struct {
	Master *master.Master
	State  Reader
	Issue  Issuer
	// RenewBefore renews that long before expiry (default 30 days).
	RenewBefore time.Duration
	// LeaseTTL is how long a master may take to issue (default 15 min).
	LeaseTTL time.Duration
	// Settle is how long a lease is left to replicate before the master
	// checks it still holds it (default 5 s).
	Settle time.Duration
	Now    func() time.Time
	Logf   func(format string, args ...any)
	// Plan runs before each tick to update what is wanted from replicated
	// state, fixing what a master decided before another master's ops arrived.
	Plan func(ctx context.Context) error
}

// Want declares a certificate for names; any master may call it.
func Want(ctx context.Context, m *master.Master, id string, names []string) error {
	if id == "" || len(names) == 0 {
		return errors.New("certs: id and names are required")
	}
	_, err := m.Set(ctx, Table, id, map[string]any{"names": names})
	return err
}

func field[T any](rec map[string]json.RawMessage, name string) (T, bool) {
	var v T
	raw, ok := rec[name]
	if !ok {
		return v, false
	}
	return v, json.Unmarshal(raw, &v) == nil
}

func (r *Renewer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Renewer) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// due lists certificates that are missing, expiring or issued for other names.
func (r *Renewer) due() []string {
	var out []string
	before := orDefault(r.RenewBefore, 30*24*time.Hour)
	for _, id := range r.State.Keys(Table) {
		rec, _ := r.State.Get(Table, id)
		names, _ := field[[]string](rec, "names")
		issuedFor, _ := field[[]string](rec, "issuedFor")
		notAfter, ok := field[int64](rec, "notAfter")
		if !ok || !slices.Equal(names, issuedFor) || time.UnixMilli(notAfter).Sub(r.now()) < before {
			out = append(out, id)
		}
	}
	return out
}

// Tick renews what is due and this master wins the lease for; it returns
// the ids it issued.
func (r *Renewer) Tick(ctx context.Context) ([]string, error) {
	var issued []string
	var errs []error
	if r.Plan != nil {
		if err := r.Plan(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for _, id := range r.due() {
		ok, err := r.lease(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		if err := r.renew(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("certs: %s: %w", id, err))
			continue
		}
		issued = append(issued, id)
	}
	return issued, errors.Join(errs...)
}

// lease claims id unless another master holds it live, lets the claim
// replicate and reports if it is still ours: the op log's last writer wins.
func (r *Renewer) lease(ctx context.Context, id string) (bool, error) {
	self := r.Master.Name()
	if rec, ok := r.State.Get(LeaseTable, id); ok {
		holder, _ := field[string](rec, "holder")
		until, _ := field[int64](rec, "until")
		if holder != self && time.UnixMilli(until).After(r.now()) {
			return false, nil
		}
	}
	until := r.now().Add(orDefault(r.LeaseTTL, 15*time.Minute)).UnixMilli()
	if _, err := r.Master.Set(ctx, LeaseTable, id, map[string]any{"holder": self, "until": until}); err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(orDefault(r.Settle, 5*time.Second)):
	}
	if _, err := r.Master.CatchUp(ctx); err != nil {
		return false, err
	}
	rec, _ := r.State.Get(LeaseTable, id)
	holder, _ := field[string](rec, "holder")
	return holder == self, nil
}

func (r *Renewer) renew(ctx context.Context, id string) error {
	rec, ok := r.State.Get(Table, id)
	if !ok {
		return errors.New("gone")
	}
	names, _ := field[[]string](rec, "names")
	res, err := r.Issue(ctx, names)
	if err != nil {
		return err
	}
	_, err = r.Master.Set(ctx, Table, id, map[string]any{
		"certPem": res.CertPEM, "keyPem": res.KeyPEM,
		"notAfter": res.NotAfter.UnixMilli(), "issuedFor": names,
	})
	if err == nil {
		r.logf("certs: %s issued for %v until %s", id, names, res.NotAfter.Format(time.DateOnly))
	}
	return err
}

// Run renews every interval until ctx ends.
func (r *Renewer) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.logf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Resolve turns certificate ids into the PEMs a node's edge serves; a
// certificate not issued yet is left out.
func Resolve(state Reader, ids []string) []nodespec.Cert {
	var out []nodespec.Cert
	for _, id := range ids {
		rec, ok := state.Get(Table, id)
		if !ok {
			continue
		}
		certPEM, ok1 := field[string](rec, "certPem")
		keyPEM, ok2 := field[string](rec, "keyPem")
		names, _ := field[[]string](rec, "issuedFor")
		if ok1 && ok2 && certPEM != "" {
			out = append(out, nodespec.Cert{Names: names, CertPEM: certPEM, KeyPEM: keyPEM})
		}
	}
	return out
}

// Status is what the panel shows for one certificate; never the key.
type Status struct {
	ID        string   `json:"id"`
	Names     []string `json:"names"`
	IssuedFor []string `json:"issuedFor,omitempty"`
	NotAfter  int64    `json:"notAfter,omitempty"`
	Holder    string   `json:"leaseHolder,omitempty"`
}

func List(state Reader) []Status {
	var out []Status
	for _, id := range state.Keys(Table) {
		rec, _ := state.Get(Table, id)
		s := Status{ID: id}
		s.Names, _ = field[[]string](rec, "names")
		s.IssuedFor, _ = field[[]string](rec, "issuedFor")
		s.NotAfter, _ = field[int64](rec, "notAfter")
		if lease, ok := state.Get(LeaseTable, id); ok {
			s.Holder, _ = field[string](lease, "holder")
		}
		out = append(out, s)
	}
	return out
}

// NodeID is the certificate a master keeps for the names only one node serves.
func NodeID(node string) string { return "node:" + node }

// Covers reports whether a certificate for names is valid for host.
func Covers(names []string, host string) bool {
	host = strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(n)
		if n == host {
			return true
		}
		if rest, ok := strings.CutPrefix(n, "*."); ok {
			if _, parent, found := strings.Cut(host, "."); found && parent == rest {
				return true
			}
		}
	}
	return false
}

// WantForNode asks for a certificate of the terminated names no other
// certificate of the node covers, and drops it once there are none.
func WantForNode(ctx context.Context, m *master.Master, state Reader, node string, terminated []string, spec []nodespec.Cert, ids []string) error {
	var covering []string
	for _, c := range spec {
		covering = append(covering, c.Names...)
	}
	for _, id := range ids {
		rec, _ := state.Get(Table, id)
		names, _ := field[[]string](rec, "names")
		covering = append(covering, names...)
	}
	var own []string
	for _, h := range terminated {
		if !Covers(covering, h) {
			own = append(own, strings.ToLower(h))
		}
	}
	slices.Sort(own)
	own = slices.Compact(own)

	id := NodeID(node)
	rec, exists := state.Get(Table, id)
	if len(own) == 0 {
		if !exists {
			return nil
		}
		_, err := m.Delete(ctx, Table, id)
		return err
	}
	// the same names must not look like a change, or every spec save re-issues
	if cur, _ := field[[]string](rec, "names"); slices.Equal(cur, own) {
		return nil
	}
	return Want(ctx, m, id, own)
}
