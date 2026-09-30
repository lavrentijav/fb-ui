// Package masterd wires one master: the bus with its gateways, the op
// projection, desired state and probes for nodes, and its own edge on 443
// that routes nodes to the bus by SNI.
package masterd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/dnsacct"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
)

// SpecTable holds each node's spec as field "spec".
const SpecTable = "nodespec"

type Config struct {
	Name     string
	Masters  []string
	StoreDir string
	// PublicName is the name nodes dial on EdgeListen; the edge splices it
	// to the bus client port.
	PublicName string
	EdgeListen string // ":443"
	ClientPort int    // bus client port on 127.0.0.1
	ClientTLS  *tls.Config
	// Gateways between masters.
	GatewayTLS  *tls.Config
	GatewayHost string
	GatewayPort int
	Advertise   string
	Peers       map[string]string
	// ACME, when set, makes this master one of those that issue and renew
	// the fleet's certificates (one at a time, by lease).
	ACME *certs.ACME
	// RenewEvery is how often due certificates are checked (default 1 h).
	RenewEvery time.Duration
	// RenewBefore renews that long before expiry (default 30 days).
	RenewBefore time.Duration
	Logf        func(format string, args ...any)
}

type Daemon struct {
	cfg   Config
	bus   *bus.Bus
	edge  *edgerun.Runner
	store *master.Mem
	m     atomic.Pointer[master.Master]
	dns   atomic.Pointer[dnsacct.Accounts]
}

// Start brings the master up; it serves nodes once the streams exist,
// which needs the other masters (JetStream quorum) the first time.
func Start(ctx context.Context, cfg Config) (*Daemon, error) {
	peers := cfg.Peers
	b, err := bus.Start(bus.Config{
		Name: cfg.Name, StoreDir: cfg.StoreDir, Host: "127.0.0.1", Port: cfg.ClientPort,
		RoutePort: cfg.ClientPort + 1000, GatewayHost: cfg.GatewayHost, GatewayPort: cfg.GatewayPort,
		Advertise: cfg.Advertise, Peers: peers, TLS: cfg.GatewayTLS, ClientTLS: cfg.ClientTLS,
	})
	if err != nil {
		return nil, err
	}
	d := &Daemon{cfg: cfg, bus: b, store: master.NewMem()}
	d.edge = &edgerun.Runner{Listen: cfg.EdgeListen, Logf: cfg.Logf}
	if err := d.edge.Apply(nodespec.Edge{SNI: []nodespec.SNIRoute{{
		Match: cfg.PublicName, Action: "tcp", Target: "127.0.0.1:" + strconv.Itoa(cfg.ClientPort),
	}}}, nil); err != nil {
		b.Close()
		return nil, err
	}
	go d.boot(ctx)
	return d, nil
}

func (d *Daemon) logf(format string, args ...any) {
	if d.cfg.Logf != nil {
		d.cfg.Logf(format, args...)
	}
}

func (d *Daemon) boot(ctx context.Context) {
	for ctx.Err() == nil {
		ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := d.bus.Ensure(ectx, d.cfg.Masters)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	m, err := master.New(ctx, d.cfg.Name, d.cfg.Masters, d.bus, d.store)
	if err != nil {
		d.logf("master: %v", err)
		return
	}
	if err := d.bus.ServeProbe(probe.SystemLoad); err != nil {
		d.logf("probe: %v", err)
	}
	if _, err := desired.Serve(d.bus.Conn(), d.cfg.Name, d.render(m)); err != nil {
		d.logf("desired: %v", err)
	}
	accounts := &dnsacct.Accounts{Master: m, State: d.store.State}
	d.dns.Store(accounts)
	d.m.Store(m)
	d.logf("master %s ready", d.cfg.Name)
	if d.cfg.ACME != nil {
		a := *d.cfg.ACME
		if a.DNSFor == nil {
			a.DNSFor = accounts.For
		}
		every := d.cfg.RenewEvery
		if every <= 0 {
			every = time.Hour
		}
		r := &certs.Renewer{
			Master: m, State: d.store.State, Issue: a.Issuer(m, d.store.State),
			RenewBefore: d.cfg.RenewBefore, Logf: d.cfg.Logf, Plan: d.planNodeCerts(m),
		}
		go r.Run(ctx, every)
	}
	m.Run(ctx, 500*time.Millisecond, func(err error) { d.logf("catch-up: %v", err) })
}

func (d *Daemon) wantNodeCert(ctx context.Context, m *master.Master, spec nodespec.Spec, b nodespec.Bundle) error {
	var terminated []string
	for _, rt := range b.Edge.SNI {
		if rt.Action == "handoff" {
			terminated = append(terminated, rt.Match)
		}
	}
	return certs.WantForNode(ctx, m, d.store.State, spec.Node, terminated, spec.Certs, spec.CertIDs)
}

// planNodeCerts redoes every node's certificate from replicated state.
func (d *Daemon) planNodeCerts(m *master.Master) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		for _, node := range d.store.State.Keys(SpecTable) {
			rec, _ := d.store.State.Get(SpecTable, node)
			var spec nodespec.Spec
			if err := json.Unmarshal(rec["spec"], &spec); err != nil {
				continue
			}
			b, err := nodespec.Render(spec)
			if err != nil {
				continue
			}
			errs = append(errs, d.wantNodeCert(ctx, m, spec, b))
		}
		return errors.Join(errs...)
	}
}

func (d *Daemon) render(m *master.Master) desired.Render {
	return func(ctx context.Context, node string) (desired.State, error) {
		rec, ok := d.store.State.Get(SpecTable, node)
		if !ok {
			return desired.State{}, fmt.Errorf("no spec for %s", node)
		}
		var spec nodespec.Spec
		if err := json.Unmarshal(rec["spec"], &spec); err != nil {
			return desired.State{}, err
		}
		// a renewal is an op like any other: it moves the version, so
		// nodes pull the new certificate with the next bundle
		ids := append(slices.Clone(spec.CertIDs), certs.NodeID(node))
		spec.Certs = append(spec.Certs, certs.Resolve(d.store.State, ids)...)
		b, err := nodespec.Render(spec)
		if err != nil {
			return desired.State{}, err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return desired.State{}, err
		}
		v, err := m.Version(ctx)
		return desired.State{Stamp: v, Config: raw}, err
	}
}

// Handler is the master's admin API (the panel API later): PUT /nodes/<node>/spec,
// PUT /certs/<id>, GET /certs, PUT|DELETE /dns/<id>, GET /dns, GET /ready, GET /reports.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		if d.m.Load() == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("PUT /nodes/{node}/spec", func(w http.ResponseWriter, r *http.Request) {
		m := d.m.Load()
		if m == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var spec nodespec.Spec
		if err := json.Unmarshal(raw, &spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node := r.PathValue("node")
		if spec.Node != node {
			http.Error(w, "spec is for "+spec.Node, http.StatusBadRequest)
			return
		}
		b, err := nodespec.Render(spec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		// the certificate first: the spec's op then already moves the version past it
		if err := d.wantNodeCert(r.Context(), m, spec, b); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		op, err := m.Set(r.Context(), SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, op.Stamp.String())
	})
	mux.HandleFunc("PUT /certs/{id}", func(w http.ResponseWriter, r *http.Request) {
		m := d.m.Load()
		if m == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := certs.Want(r.Context(), m, r.PathValue("id"), body.Names); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	})
	mux.HandleFunc("PUT /dns/{id}", func(w http.ResponseWriter, r *http.Request) {
		accounts := d.dns.Load()
		if accounts == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var acc dnsacct.Account
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&acc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := accounts.Put(r.Context(), r.PathValue("id"), acc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved.Credential = ""
		_ = json.NewEncoder(w).Encode(saved)
	})
	mux.HandleFunc("DELETE /dns/{id}", func(w http.ResponseWriter, r *http.Request) {
		accounts := d.dns.Load()
		if accounts == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if err := accounts.Delete(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("GET /dns", func(w http.ResponseWriter, _ *http.Request) {
		if accounts := d.dns.Load(); accounts != nil {
			_ = json.NewEncoder(w).Encode(accounts.List())
		}
	})
	mux.HandleFunc("GET /certs", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(certs.List(d.store.State))
	})
	mux.HandleFunc("GET /reports", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]uint64{}
		for _, m := range d.cfg.Masters {
			if _, last, err := d.bus.ReportRange(r.Context(), m); err == nil {
				out[m] = last
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

func (d *Daemon) Close() error {
	d.bus.Close()
	return errors.Join(d.edge.Close())
}
