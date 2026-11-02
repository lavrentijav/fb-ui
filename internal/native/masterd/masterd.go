// Package masterd wires one master: the bus with its gateways, the op
// projection, desired state and probes for nodes, and its own edge on 443
// that routes nodes to the bus by SNI.
package masterd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/dnsacct"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/groups"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/inbounds"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/mhost"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestate"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pool"
	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
	"github.com/mhsanaei/3x-ui/v3/internal/native/provision"
	"github.com/mhsanaei/3x-ui/v3/internal/native/register"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/submeta"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templates"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

// SpecTable holds each node's spec as field "spec".
const SpecTable = "nodespec"

// SubTokenTable maps a subscription token to the client's email.
const SubTokenTable = "subtoken"

// FleetTable holds fleet-wide values; key "servers" is the cascade client id
// every exit uses and every entry dials.
const FleetTable = "fleetid"

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
	// PoolName is the masters' shared name (master.firewalk.top), resolving to
	// every live master; empty keeps this master out of any pool.
	PoolName string
	// PublicIP is this master's address in the pool; PublicIPs carries several
	// (a host reached by more than one address). Empty means the master detects
	// its own addresses and keeps them up to date.
	PublicIP  string
	PublicIPs []string
	PoolRoots *x509.CertPool // what the pool check trusts (nil: the system's)
	PoolEvery time.Duration  // default 20 s
	// PoolMaxDownFraction is the share of masters that must fail an address
	// before it leaves DNS (default 0.5); PoolMaxRecords caps how many
	// addresses a name resolves to (0 = every live one).
	PoolMaxDownFraction float64
	PoolMaxRecords      int
	// FleetCA, when set, lets masters enrol nodes: a one-time token becomes
	// a node.<name> certificate signed by this CA.
	FleetCA *fleetca.CA
	// NodeBinary is a local fbnode build that POST /provision/install uploads
	// to a server; empty disables installing.
	NodeBinary string
	// UIDir is a built interface (an index.html with its assets); when set,
	// the master serves it at /panel and the old /panel/api surface next to
	// /api/v1, so the interface talks to the native master.
	UIDir string
	// APIListen is where /api/v1 is served (for example "127.0.0.1:8443");
	// empty serves no API and runs no modules' routes. Modules still run.
	APIListen string
	// AdminListen is the master's administrative HTTP surface (subscriptions,
	// hosts, nodes, enroll). Empty means loopback on a free port; a non-loopback
	// address is refused unless AdminAllowPublic is set.
	AdminListen      string
	AdminAllowPublic bool
	Logf             func(format string, args ...any)
}

type Daemon struct {
	cfg      Config
	bus      *bus.Bus
	edge     *edgerun.Runner
	store    *master.Mem
	m        atomic.Pointer[master.Master]
	dns      atomic.Pointer[dnsacct.Accounts]
	host     atomic.Pointer[mhost.Host]
	vnets    atomic.Pointer[vnet.Store]
	pool     atomic.Pointer[pool.Pool]
	presence *presence.Map
	api      *http.Server
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
	d := &Daemon{cfg: cfg, bus: b, store: master.NewMem(), presence: &presence.Map{}}
	d.edge = &edgerun.Runner{Listen: cfg.EdgeListen, Logf: cfg.Logf}
	if err := d.edge.Apply(d.busEdge(), nil); err != nil {
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
	d.vnets.Store(&vnet.Store{Master: m, State: d.store.State})
	if _, err := d.presence.Watch(d.bus.Conn()); err != nil {
		d.logf("presence: %v", err)
	}
	if _, err := desired.Serve(d.bus.Conn(), d.cfg.Name, d.render(m)); err != nil {
		d.logf("desired: %v", err)
	}
	accounts := &dnsacct.Accounts{Master: m, State: d.store.State}
	d.dns.Store(accounts)
	d.m.Store(m)
	d.logf("master %s ready", d.cfg.Name)
	d.startModules(ctx, m)
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
	if d.cfg.PoolName != "" {
		go d.runPool(ctx, m, accounts)
	}
	m.Run(ctx, 500*time.Millisecond, func(err error) { d.logf("catch-up: %v", err) })
}

func poolCertID(name string) string { return "pool:" + name }

// runPool puts this master in the pool: its address and the pool's
// certificate go to the op log, its edge serves the pool name once the
// certificate is there, and it checks the others for the pool's records.
func (d *Daemon) runPool(ctx context.Context, m *master.Master, accounts *dnsacct.Accounts) {
	name := d.cfg.PoolName
	for ctx.Err() == nil {
		err := errors.Join(d.announceIPs(ctx, m), d.wantPoolCert(ctx, m))
		if err == nil {
			break
		}
		d.logf("pool: %v", err)
		time.Sleep(time.Second)
	}
	// a moved machine or a new public address is picked up without a restart
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := d.announceIPs(ctx, m); err != nil {
					d.logf("pool: %v", err)
				}
			}
		}
	}()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		d.logf("pool: %v", err)
		return
	}
	srv := &http.Server{Handler: d.Public(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	go d.serveEdge(ctx, "http://"+ln.Addr().String())

	dnsFor := accounts.For
	if d.cfg.ACME != nil && d.cfg.ACME.DNSFor != nil {
		dnsFor = d.cfg.ACME.DNSFor
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: name, RootCAs: d.cfg.PoolRoots, MinVersion: tls.VersionTLS12},
	}}
	p := &pool.Pool{
		Master: m, State: d.store.State, Name: name, DNS: dnsFor, Logf: d.cfg.Logf,
		MaxDownFraction: d.cfg.PoolMaxDownFraction, MaxRecords: d.cfg.PoolMaxRecords,
		Check: func(ctx context.Context, ip string) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(ip, "443")+"/healthz", nil)
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthz: %s", resp.Status)
			}
			return nil
		},
	}
	d.pool.Store(p)
	every := d.cfg.PoolEvery
	if every <= 0 {
		every = 20 * time.Second
	}
	p.Run(ctx, every)
}

// announceIPs writes this master's pool addresses: an operator's manual list,
// the configured addresses, or the ones it detects on its own.
func (d *Daemon) announceIPs(ctx context.Context, m *master.Master) error {
	if _, manual := pool.Manual(d.store.State, m.Name()); manual {
		return nil
	}
	switch {
	case len(d.cfg.PublicIPs) > 0:
		return pool.Announce(ctx, m, d.store.State, d.cfg.PublicIPs)
	case d.cfg.PublicIP != "":
		return pool.Announce(ctx, m, d.store.State, []string{d.cfg.PublicIP})
	default:
		return pool.AnnounceAuto(ctx, m, d.store.State, pool.DetectIPs())
	}
}

func (d *Daemon) wantPoolCert(ctx context.Context, m *master.Master) error {
	id := poolCertID(d.cfg.PoolName)
	if rec, ok := d.store.State.Get(certs.Table, id); ok {
		var names []string
		if json.Unmarshal(rec["names"], &names) == nil && slices.Equal(names, []string{d.cfg.PoolName}) {
			return nil
		}
	}
	return certs.Want(ctx, m, id, []string{d.cfg.PoolName})
}

// serveEdge keeps the edge's routes: the bus by SNI always, the pool name
// to the public handler once its certificate is issued or renewed.
func (d *Daemon) serveEdge(ctx context.Context, public string) {
	var served string
	for ctx.Err() == nil {
		got := certs.Resolve(d.store.State, []string{poolCertID(d.cfg.PoolName)})
		if len(got) == 1 && got[0].CertPEM != served {
			e := d.busEdge()
			e.SNI = append(e.SNI, nodespec.SNIRoute{Match: d.cfg.PoolName, Action: "handoff", Target: nodespec.Handoff})
			e.HTTP = []nodespec.HTTPRoute{{Host: d.cfg.PoolName, PathPrefix: "/", Target: public}}
			if err := d.edge.Apply(e, got); err != nil {
				d.logf("pool edge: %v", err)
			} else {
				served = got[0].CertPEM
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func (d *Daemon) busEdge() nodespec.Edge {
	return nodespec.Edge{SNI: []nodespec.SNIRoute{{
		Match: d.cfg.PublicName, Action: "tcp", Target: "127.0.0.1:" + strconv.Itoa(d.cfg.ClientPort),
	}}}
}

// Public is what clients reach on the pool name; subscriptions and the panel
// come here next.
func (d *Daemon) Public() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if d.m.Load() == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok", d.cfg.Name)
	})
	mux.Handle("/api/v1/", http.HandlerFunc(d.vnetAPI))
	mux.HandleFunc("GET /sub/{token}", d.serveSub)
	d.mountSubAPI(mux)
	d.mountEnroll(mux)
	d.mountUI(mux)
	return mux
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
		if s := d.vnets.Load(); s != nil {
			specs := map[string]nodespec.Spec{}
			for _, other := range d.specs() {
				specs[other.Node] = other
			}
			s.Fill(&spec, specs)
		}
		// a renewal is an op like any other: it moves the version, so
		// nodes pull the new certificate with the next bundle
		ids := append(slices.Clone(spec.CertIDs), certs.NodeID(node))
		spec.Certs = append(spec.Certs, certs.Resolve(d.store.State, ids)...)
		contribute(module.All(), d.store.State, node, &spec)
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
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if d.m.Load() == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok "+d.cfg.Name+"\n")
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
	mux.HandleFunc("GET /sub", d.listSubscriptions)
	mux.HandleFunc("PUT /sub/{email}", d.createSubscription)
	mux.HandleFunc("POST /sub/{email}/password", d.resetSubPassword)
	mux.HandleFunc("POST /sub/{token}/enable", d.setSubEnabled)
	mux.HandleFunc("GET /vnet/networks", d.listNetworks)
	mux.HandleFunc("DELETE /sub/{token}", func(w http.ResponseWriter, r *http.Request) {
		m := d.m.Load()
		if m == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if _, err := m.Delete(r.Context(), SubTokenTable, r.PathValue("token")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("GET /sub/{token}", d.serveSub)
	d.mountSubAPI(mux)
	d.mountEnroll(mux)
	d.mountUI(mux)
	mux.HandleFunc("POST /provision/preflight", d.preflight)
	mux.HandleFunc("POST /provision/install", d.install)
	mux.HandleFunc("POST /provision/register", d.registerNode)
	mux.HandleFunc("GET /nodes/state", d.nodesState)
	mux.HandleFunc("GET /net/ips", d.netIPs)
	mux.HandleFunc("PUT /net/ips", d.setNetIPs)
	mux.HandleFunc("GET /net/health", d.netHealth)
	mux.HandleFunc("POST /nodes/{node}/drain", d.setDrain)
	mux.HandleFunc("GET /inbounds/{node}", func(w http.ResponseWriter, r *http.Request) {
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		spec, ok := inbounds.Spec(d.store.State, r.PathValue("node"))
		if !ok {
			http.Error(w, "no spec for that node", http.StatusNotFound)
			return
		}
		apiserver.JSON(w, http.StatusOK, spec.Inbounds)
	})
	mux.HandleFunc("POST /inbounds/{node}", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var in inbounds.Input
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		ib, err := inbounds.Add(r.Context(), h, d.store.State, r.PathValue("node"), in)
		if err != nil {
			code := http.StatusBadRequest
			if err == inbounds.ErrExists {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		apiserver.JSON(w, http.StatusCreated, ib)
	})
	mux.HandleFunc("PUT /inbounds/{node}/{tag}", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var in inbounds.Input
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		ib, err := inbounds.Update(r.Context(), h, d.store.State, r.PathValue("node"), r.PathValue("tag"), in)
		if err != nil {
			code := http.StatusBadRequest
			if err == inbounds.ErrNotFound {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		apiserver.JSON(w, http.StatusOK, ib)
	})
	mux.HandleFunc("DELETE /inbounds/{node}/{tag}", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		if err := inbounds.Remove(r.Context(), h, d.store.State, r.PathValue("node"), r.PathValue("tag")); err != nil {
			code := http.StatusBadRequest
			if err == inbounds.ErrNotFound {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /groups", func(w http.ResponseWriter, r *http.Request) {
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		apiserver.JSON(w, http.StatusOK, groups.Summaries(d.store.State, time.Now()))
	})
	mux.HandleFunc("POST /groups", d.saveGroup)
	mux.HandleFunc("POST /groups/{name}/{op}", d.groupOp)
	mux.HandleFunc("DELETE /groups/{name}", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if err := groups.Delete(r.Context(), h, r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		for _, mod := range module.All() {
			if dm, ok := mod.(interface {
				Summary(context.Context, module.Host) any
			}); ok {
				apiserver.JSON(w, http.StatusOK, dm.Summary(r.Context(), h))
				return
			}
		}
		http.Error(w, "no dashboard module", http.StatusNotImplemented)
	})
	mux.HandleFunc("GET /usage", func(w http.ResponseWriter, r *http.Request) {
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		out := map[string]int64{}
		for _, email := range d.store.State.Keys(usage.Table) {
			rec, ok := d.store.State.Get(usage.Table, email)
			if !ok {
				continue
			}
			var n int64
			_ = json.Unmarshal(rec["bytes"], &n)
			out[email] = n
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, clients.List(d.store.State))
	})
	mux.HandleFunc("PUT /clients", d.createClient)
	mux.HandleFunc("POST /clients", d.saveClient)
	mux.HandleFunc("DELETE /clients/{node}/{inbound}/{email}", d.deleteClient)
	mux.HandleFunc("GET /hosts", func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, hosts.List(d.store.State))
	})
	mux.HandleFunc("POST /hosts", d.saveHost)
	mux.HandleFunc("DELETE /hosts/{id}", func(w http.ResponseWriter, r *http.Request) {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		for _, e := range hosts.List(d.store.State) {
			if e.ID == id {
				if err := hosts.Delete(r.Context(), h, e); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
		}
		http.Error(w, "no such host entry", http.StatusNotFound)
	})
	mux.HandleFunc("GET /templates", func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, templates.List(d.store.State))
	})
	mux.HandleFunc("POST /templates", d.saveTemplate)
	mux.HandleFunc("DELETE /templates/{name}", func(w http.ResponseWriter, r *http.Request) {
		m := d.m.Load()
		if m == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if err := templates.Delete(r.Context(), m, r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /templates/{name}/apply", d.applyTemplate)
	mux.HandleFunc("PUT /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := d.vnets.Load()
		if s == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		u, err := s.CreateUser(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(u)
	})
	mux.Handle("/api/v1/", http.HandlerFunc(d.vnetAPI))
	return mux
}

// preflight inspects a server over SSH before anything is installed. It is
// on the admin API only: it takes credentials, so the admin API must stay on
// loopback until the session login exists.
func (d *Daemon) preflight(w http.ResponseWriter, r *http.Request) {
	var body struct {
		provision.Target
		Auth    provision.Auth `json:"auth"`
		HostKey string         `json:"hostKey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := provision.Preflight(ctx, body.Target, body.Auth, body.HostKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(res)
}

// install puts the agent on a server: it redeems an identity for node, then
// uploads the binary, the identity and a unit over SSH and starts it. Like
// preflight it takes credentials, so it lives on the admin API only.
func (d *Daemon) install(w http.ResponseWriter, r *http.Request) {
	if d.cfg.FleetCA == nil || d.cfg.NodeBinary == "" {
		http.Error(w, "installing is not configured (needs a fleet CA and a node binary)", http.StatusNotImplemented)
		return
	}
	var body struct {
		provision.Target
		Auth    provision.Auth    `json:"auth"`
		HostKey string            `json:"hostKey"`
		Node    string            `json:"node"`
		Masters map[string]string `json:"masters"`
		Dir     string            `json:"dir"`
		Bin     string            `json:"bin"`
		Unit    string            `json:"unit"`
		XrayBin string            `json:"xrayBin"`
		Wait    string            `json:"wait"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	binary, err := os.ReadFile(d.cfg.NodeBinary)
	if err != nil {
		http.Error(w, "the node binary: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wait, err := time.ParseDuration(body.Wait)
	if err != nil || wait <= 0 {
		wait = 30 * time.Second
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	secret := hex.EncodeToString(buf)
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := enroll.Mint(ctx, enrollStore{d.m.Load}, secret, body.Node, time.Hour, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	identity, err := enroll.Redeem(ctx, d.cfg.FleetCA, enrollStore{d.m.Load}, d.store.State, body.Node, secret, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res, err := provision.Install(ctx, provision.InstallRequest{
		Target: body.Target, Auth: body.Auth, HostKey: body.HostKey,
		Node: body.Node, Masters: body.Masters, Identity: identity, Binary: binary,
		Dir: body.Dir, BinPath: body.Bin, UnitPath: body.Unit, XrayBin: body.XrayBin, Wait: wait,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(res)
}

// mountUI serves the built interface and the old panel API it calls, at
// /panel and /panel/api. It needs the master's modules, so it is a no-op
// until the master is ready.
func (d *Daemon) mountUI(mux *http.ServeMux) {
	if d.cfg.UIDir == "" {
		return
	}
	mux.Handle("/panel/", http.HandlerFunc(d.serveUI))
	mux.HandleFunc("GET /panel", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusFound)
	})
}

// serveUI answers the interface's files, falling back to index.html so the
// client-side router owns the paths, and hands /panel/api to the modules'
// legacy adapter.
func (d *Daemon) serveUI(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/panel/api/") {
		h := d.host.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		withLegacy(module.All(), apiserver.New(h, module.All())).ServeHTTP(w, r)
		return
	}
	file := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/panel"), "/")
	file = path.Clean("/" + file)
	if file == "/" || file == "/index.html" {
		d.serveIndex(w, r)
		return
	}
	full := filepath.Join(d.cfg.UIDir, file)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		d.serveIndex(w, r) // a client-side route
		return
	}
	http.ServeFile(w, r, full)
}

func (d *Daemon) serveIndex(w http.ResponseWriter, r *http.Request) {
	index := filepath.Join(d.cfg.UIDir, "index.html")
	b, err := os.ReadFile(index)
	if err != nil {
		http.Error(w, "no interface built in "+d.cfg.UIDir, http.StatusNotFound)
		return
	}
	html := string(b)
	// the page logs in with an API token here, not a user and password
	if !strings.Contains(html, "X_UI_NATIVE") {
		html = strings.Replace(html, "</head>", "<script>window.X_UI_NATIVE=true;</script></head>", 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, html)
}

// clientInput is the admin API's shape; expiry accepts RFC3339 or unix ms.
type clientInput struct {
	clients.Client
	Expiry string `json:"expiry"`
}

func (in clientInput) client() (clients.Client, error) {
	c := in.Client
	if in.Expiry != "" {
		ms, err := clients.ParseExpiry(in.Expiry)
		if err != nil {
			return clients.Client{}, err
		}
		c.ExpiresAt = ms
	}
	return c, nil
}

// createClient refuses an email already on that inbound.
func (d *Daemon) createClient(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var in clientInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c, err := in.client()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := clients.Create(r.Context(), h, c)
	if err != nil {
		code := http.StatusBadRequest
		if err == clients.ErrExists {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	apiserver.JSON(w, http.StatusCreated, saved)
}

// saveClient creates or replaces one record.
func (d *Daemon) saveClient(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var in clientInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c, err := in.client()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, _, err := clients.Save(r.Context(), h, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserver.JSON(w, http.StatusOK, saved)
}

func (d *Daemon) deleteClient(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	if err := clients.Delete(r.Context(), h, r.PathValue("node"), r.PathValue("inbound"), r.PathValue("email")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// saveGroup creates or replaces a client group.
func (d *Daemon) saveGroup(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name      string   `json:"name"`
		Remark    string   `json:"remark"`
		Emails    []string `json:"emails"`
		Overwrite bool     `json:"overwrite"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g, err := groups.Save(r.Context(), h, groups.Group{Name: body.Name, Remark: body.Remark, Emails: body.Emails}, body.Overwrite)
	if err != nil {
		code := http.StatusBadRequest
		if err == groups.ErrExists {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	apiserver.JSON(w, http.StatusOK, g)
}

// groupOp runs one bulk operation: members, enable, attach, detach.
func (d *Daemon) groupOp(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	name, op := r.PathValue("name"), r.PathValue("op")
	var body struct {
		Emails  []string `json:"emails"`
		Add     bool     `json:"add"`
		Enable  bool     `json:"enable"`
		Node    string   `json:"node"`
		Inbound string   `json:"inbound"`
		UUID    string   `json:"uuid"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	var out any
	var err error
	code := http.StatusBadRequest
	switch op {
	case "members":
		var g groups.Group
		g, err = groups.SetMembers(r.Context(), h, name, body.Emails, body.Add)
		out = g
	case "enable":
		var n int
		n, err = groups.SetEnabled(r.Context(), h, name, body.Enable)
		out = map[string]int{"changed": n}
	case "attach":
		var n int
		n, err = groups.Attach(r.Context(), h, name, body.Node, body.Inbound, body.UUID)
		out = map[string]int{"created": n}
	case "detach":
		var n int
		n, err = groups.Detach(r.Context(), h, name, body.Node, body.Inbound)
		out = map[string]int{"removed": n}
	default:
		http.Error(w, "unknown operation "+op, http.StatusNotFound)
		return
	}
	if err != nil {
		if err == groups.ErrNoGroup {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	apiserver.JSON(w, http.StatusOK, out)
}

// saveHost stores one advertised address for a node's inbound.
func (d *Daemon) saveHost(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var e hosts.Entry
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	saved, err := hosts.Save(r.Context(), h, e)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserver.JSON(w, http.StatusOK, saved)
}

// saveTemplate stores a spec template (a shape without clients).
func (d *Daemon) saveTemplate(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body templates.Save
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, _ = m.CatchUp(r.Context())
	t, err := templates.SaveTemplate(r.Context(), m, d.store.State, body)
	if err != nil {
		code := http.StatusBadRequest
		if err == templates.ErrExists {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	apiserver.JSON(w, http.StatusOK, t)
}

// applyTemplate stamps a template onto nodes; each applied spec goes through
// the normal write, so a node picks it up on its next pull.
func (d *Daemon) applyTemplate(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body templates.ApplyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if !d.waitKey(r.Context(), m, templates.Table, name, 60*time.Second) {
		http.Error(w, "no such template (not replicated here yet)", http.StatusNotFound)
		return
	}
	out, err := templates.Stamp(r.Context(), d.store.State, m, name, body)
	if err != nil {
		code := http.StatusBadRequest
		if err == templates.ErrNoTemplate {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	apiserver.JSON(w, http.StatusOK, out)
}

// setDrain puts a node in or out of maintenance through its spec; the agent
// refuses new tunnels while it drains and lets open ones finish.
func (d *Daemon) setDrain(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		On   bool   `json:"on"`
		Idle string `json:"idle"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	node := r.PathValue("node")
	rec, ok := d.store.State.Get(SpecTable, node)
	if !ok {
		http.Error(w, "no spec for "+node, http.StatusNotFound)
		return
	}
	var spec nodespec.Spec
	if err := json.Unmarshal(rec["spec"], &spec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	spec.Drain = nodespec.Drain{On: body.On, Idle: body.Idle}
	if _, err := nodespec.Render(spec); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op, err := m.Set(r.Context(), SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"node": node, "stamp": op.Stamp.String(), "drain": spec.Drain})
}

// nodesState is the fleet as the interface shows it: every node with a spec
// plus every node that reported, why it is online and whether it drains.
func (d *Daemon) nodesState(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	_, _ = m.CatchUp(r.Context())
	nodes := append([]string(nil), d.store.State.Keys(SpecTable)...)
	reps := nodestate.Collect(r.Context(), d.bus, d.cfg.Masters, 2000)
	apiserver.JSON(w, http.StatusOK, nodestate.Aggregate(nodes, reps, time.Now(), nodestate.DefaultStale))
}

// registerNode builds a node's spec from a role and a public name and writes
// it, so an installed node starts serving with its first pull.
func (d *Daemon) registerNode(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		register.Request
		Overwrite bool   `json:"overwrite"`
		Wait      string `json:"wait"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	_, _ = m.CatchUp(ctx)
	if !body.Overwrite {
		_, exists := d.store.State.Get(SpecTable, body.Node)
		// a spec written a moment ago on another master may still be in the
		// mirror; give it a short window so a second register is a conflict
		if !exists {
			exists = d.waitSpec(ctx, m, body.Node, 2*time.Second)
		}
		if exists {
			http.Error(w, "a spec for "+body.Node+" already exists (set overwrite)", http.StatusConflict)
			return
		}
	}
	serversID, err := d.serversID(ctx, m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if body.CascadeTo != "" && !d.waitSpec(ctx, m, body.CascadeTo, 60*time.Second) {
		http.Error(w, "no spec for exit "+body.CascadeTo+" (not registered or not replicated yet)", http.StatusUnprocessableEntity)
		return
	}
	peers := map[string]nodespec.Spec{}
	for _, s := range d.specs() {
		peers[s.Node] = s
	}
	spec, err := register.Build(body.Request, serversID, peers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	b, err := nodespec.Render(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := d.wantNodeCert(ctx, m, spec, b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err := register.Encode(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := m.Set(ctx, SpecTable, spec.Node, map[string]any{"spec": raw}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reported := false
	if wait, _ := time.ParseDuration(body.Wait); wait > 0 {
		reported = d.waitReport(ctx, spec.Node, wait)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"node": spec.Node, "summary": register.Describe(spec), "spec": spec, "reported": reported,
	})
}

// waitKey waits for a record written on another master to replicate here.
func (d *Daemon) waitKey(ctx context.Context, m *master.Master, table, key string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		_, _ = m.CatchUp(ctx)
		if _, ok := d.store.State.Get(table, key); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// waitSpec waits for a spec written on another master to replicate here.
func (d *Daemon) waitSpec(ctx context.Context, m *master.Master, node string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		_, _ = m.CatchUp(ctx)
		if _, ok := d.store.State.Get(SpecTable, node); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// serversID is the fleet's cascade identity, created on first use.
func (d *Daemon) serversID(ctx context.Context, m *master.Master) (string, error) {
	for _, key := range d.store.State.Keys(FleetTable) {
		if key != "servers" {
			continue
		}
		rec, _ := d.store.State.Get(FleetTable, key)
		var id string
		if json.Unmarshal(rec["uuid"], &id) == nil && id != "" {
			return id, nil
		}
	}
	id, err := register.NewServersID()
	if err != nil {
		return "", err
	}
	_, err = m.Set(ctx, FleetTable, "servers", map[string]any{"uuid": id})
	return id, err
}

// waitReport waits until a recent report of node appears in any master's
// report stream; it only looks at the newest scan entries, which is enough
// while a node reports every few seconds.
func (d *Daemon) waitReport(ctx context.Context, node string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		for _, master := range d.cfg.Masters {
			first, last, err := d.bus.ReportRange(ctx, master)
			if err != nil || last == 0 {
				continue
			}
			from := uint64(1)
			if last > 200 {
				from = last - 200
			}
			if first > from {
				from = first
			}
			for seq := last; seq >= from && seq > 0; seq-- {
				raw, err := d.bus.GetReport(ctx, master, seq)
				if err != nil {
					break
				}
				var rep struct {
					Node string `json:"n"`
				}
				if json.Unmarshal(raw, &rep) == nil && rep.Node == node {
					return true
				}
				if seq == from {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// mountEnroll serves token minting and redemption once a CA is configured.
func (d *Daemon) mountEnroll(mux *http.ServeMux) {
	if d.cfg.FleetCA == nil {
		return
	}
	redeem := enroll.Handler(d.cfg.FleetCA, enrollStore{d.m.Load}, d.store.State, time.Now)
	// a token minted on another master may not have replicated here yet
	mux.Handle("POST /enroll", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := d.m.Load(); m != nil {
			_, _ = m.CatchUp(r.Context())
		}
		redeem.ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /enroll/tokens", func(w http.ResponseWriter, r *http.Request) {
		m := d.m.Load()
		if m == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Node string `json:"node"`
			TTL  string `json:"ttl"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ttl, err := time.ParseDuration(body.TTL)
		if err != nil || ttl <= 0 {
			ttl = time.Hour
		}
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		secret := hex.EncodeToString(buf)
		if err := enroll.Mint(r.Context(), enrollStore{d.m.Load}, secret, body.Node, ttl, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"node": body.Node, "token": enroll.Token(secret, d.cfg.FleetCA), "caPin": enroll.Pin(d.cfg.FleetCA),
		})
	})
}

// enrollStore adapts *master.Master to enroll.Writer.
type enrollStore struct{ m func() *master.Master }

func (s enrollStore) Set(ctx context.Context, table, key string, fields map[string]any) error {
	_, err := s.m().Set(ctx, table, key, fields)
	return err
}

func (s enrollStore) Delete(ctx context.Context, table, key string) error {
	_, err := s.m().Delete(ctx, table, key)
	return err
}

// effectiveSpecs is every node's spec with what the modules add when a master
// renders it (network members, clients, routing rules), so the subscription
// sees the same inbounds the node runs.
func (d *Daemon) effectiveSpecs() []nodespec.Spec {
	raw := d.specs()
	byNode := make(map[string]nodespec.Spec, len(raw))
	for _, s := range raw {
		byNode[s.Node] = s
	}
	out := make([]nodespec.Spec, 0, len(raw))
	for _, s := range raw {
		if st := d.vnets.Load(); st != nil {
			st.Fill(&s, byNode)
		}
		contribute(module.All(), d.store.State, s.Node, &s)
		out = append(out, s)
	}
	return out
}

// specs is every node's spec from the projection.
func (d *Daemon) specs() []nodespec.Spec {
	var out []nodespec.Spec
	for _, node := range d.store.State.Keys(SpecTable) {
		rec, ok := d.store.State.Get(SpecTable, node)
		var s nodespec.Spec
		if ok && json.Unmarshal(rec["spec"], &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// subToken answers the subscription token for email, creating it (and its
// login) when missing. user defaults to the email; a non-empty pass sets a new
// password and overwrites the login. The password is never returned here — the
// callers that mint one answer it once.
func (d *Daemon) subToken(ctx context.Context, m *master.Master, email, user, passHash string) (string, error) {
	// a token minted on another master may not have replicated here yet
	_, _ = m.CatchUp(ctx)
	var token string
	for _, key := range d.store.State.Keys(SubTokenTable) {
		rec, ok := d.store.State.Get(SubTokenTable, key)
		var have string
		if ok && json.Unmarshal(rec["email"], &have) == nil && have == email {
			token = key
			break
		}
	}
	if token == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		token = hex.EncodeToString(buf)
	}
	fields := map[string]any{"email": email, "enable": true}
	if rec, ok := d.store.State.Get(SubTokenTable, token); ok {
		var enabled bool
		_ = json.Unmarshal(rec["enable"], &enabled)
		fields["enable"] = enabled
		if u, _ := rec["user"]; len(u) > 0 {
			var have string
			if json.Unmarshal(u, &have) == nil {
				fields["user"] = have
			}
		}
		if p, _ := rec["pass"]; len(p) > 0 {
			var have string
			if json.Unmarshal(p, &have) == nil {
				fields["pass"] = have
			}
		}
	}
	if user != "" {
		fields["user"] = user
	} else if _, ok := fields["user"]; !ok {
		fields["user"] = email
	}
	if passHash != "" {
		fields["pass"] = passHash
	}
	_, err := m.Set(ctx, SubTokenTable, token, fields)
	return token, err
}

// wantsHTML reports whether the caller is a browser rather than a client.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if v := r.URL.Query().Get("format"); v != "" {
		return v == "page"
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// serveSub is a client's subscription: a browser gets the page, a client the
// base64 link list.
func (d *Daemon) serveSub(w http.ResponseWriter, r *http.Request) {
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	token := r.PathValue("token")
	rec, ok := subauth.Get(d.store.State, token)
	if !ok || !rec.Enable {
		http.NotFound(w, r)
		return
	}
	rendered := d.renderSub(rec, token, r)
	if wantsHTML(r) {
		d.serveSubPage(w, r, rendered)
		return
	}
	if len(rendered.links) == 0 {
		http.Error(w, "no inbounds for "+rec.Email, http.StatusNotFound)
		return
	}
	writeSubHeaders(w, rendered.info)
	// ?format=plain is the metadata lines plus the links, for a client or CDN
	// that drops headers; the default stays base64 for the older clients.
	if subFormat(r) == "plain" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, rendered.info.Plain(rendered.links))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, sub.Base64(rendered.links))
}

// subPageData is what the subscription page reads from window.
type subPageData struct {
	SID        string   `json:"sId"`
	User       string   `json:"user"`
	Enabled    bool     `json:"enabled"`
	Links      []string `json:"links"`
	Emails     []string `json:"emails"`
	SubURL     string   `json:"subUrl"`
	SubJSONURL string   `json:"subJsonUrl"`
	SubTitle   string   `json:"subTitle"`
	UsedByte   int64    `json:"usedByte"`
	TotalByte  int64    `json:"totalByte"`
	Expire     int64    `json:"expire"`
	Datepicker string   `json:"datepicker"`
	// The same metadata the response headers carry, for the page itself.
	UploadByte    int64           `json:"uploadByte"`
	DownloadByte  int64           `json:"downloadByte"`
	SupportURL    string          `json:"supportUrl,omitempty"`
	WebPageURL    string          `json:"webPageUrl,omitempty"`
	Announce      string          `json:"announce,omitempty"`
	AnnounceURL   string          `json:"announceUrl,omitempty"`
	AnnounceColor string          `json:"announceColor,omitempty"`
	UpdateHours   int             `json:"updateHours,omitempty"`
	RefillDate    int64           `json:"refillDate,omitempty"`
	HwidLimit     int             `json:"hwidLimit,omitempty"`
	Servers       []subPageServer `json:"servers,omitempty"`
	BackupURLs    []string        `json:"backupUrls,omitempty"`
}

// subPageServer is one server's share of the traffic, for the page's list.
type subPageServer struct {
	Name     string `json:"name"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// serveSubPage serves the built subscription page (UIDir/subpage.html) with
// the subscription baked in. Assets are absolute (/assets/...), so they are
// pointed at the panel's static mount.
func (d *Daemon) serveSubPage(w http.ResponseWriter, r *http.Request, rendered subRender) {
	if d.cfg.UIDir == "" {
		http.Error(w, "no interface built", http.StatusNotFound)
		return
	}
	page, err := os.ReadFile(filepath.Join(d.cfg.UIDir, "subpage.html"))
	if err != nil {
		http.Error(w, "no subscription page built", http.StatusNotFound)
		return
	}
	info := rendered.info
	rec, _ := subauth.Get(d.store.State, rendered.token)
	servers := make([]subPageServer, 0, len(info.Servers))
	for _, name := range submeta.SortedRemarks(info.Servers) {
		q := info.Servers[name]
		servers = append(servers, subPageServer{Name: name, Upload: q.Upload, Download: q.Download})
	}
	data := subPageData{
		SID: rendered.token, User: rec.User, Enabled: rec.Enable, Links: rendered.links,
		Emails: []string{rec.Email},
		SubURL: r.URL.Path, SubJSONURL: r.URL.Path + "?format=json",
		SubTitle: info.Title, UsedByte: info.Quota.Used(), TotalByte: info.Quota.Total,
		Expire: info.Quota.Expire, Datepicker: "gregorian",
		UploadByte: info.Quota.Upload, DownloadByte: info.Quota.Download,
		SupportURL: info.SupportURL, WebPageURL: info.WebPageURL,
		Announce: info.Announce.Text, AnnounceURL: info.Announce.URL,
		AnnounceColor: info.Announce.Color, UpdateHours: info.UpdateHours,
		RefillDate: info.RefillDate, HwidLimit: info.HwidLimit,
		Servers: servers, BackupURLs: info.BackupURLs,
	}
	baked, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	html := string(page)
	if i := strings.Index(html, `/assets/`); i >= 0 {
		html = strings.ReplaceAll(html, `"/assets/`, `"/panel/assets/`)
		html = strings.ReplaceAll(html, `href="/assets/`, `href="/panel/assets/`)
	}
	inject := `<script>window.X_UI_BASE_PATH="/";window.__SUB_PAGE_DATA__=` + string(baked) + `;window.__SUB_MANAGE__="/api/v1/sub";</script></head>`
	html = strings.Replace(html, "</head>", inject, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}

// emailUsage sums an email's counted traffic and the allowance/expiry of its
// first client record.
func (d *Daemon) emailUsage(email string) (used, total, expire int64) {
	if rec, ok := d.store.State.Get(usage.Table, email); ok {
		_ = json.Unmarshal(rec["bytes"], &used)
	}
	for _, c := range clients.List(d.store.State) {
		if c.Email != email {
			continue
		}
		if c.TotalGB > total {
			total = c.TotalGB
		}
		if c.ExpiresAt != 0 && (expire == 0 || c.ExpiresAt < expire) {
			expire = c.ExpiresAt / 1000
		}
	}
	return
}

// createSubscription creates or updates a subscription: PUT /sub/{email} with
// an optional {"user","password"}. A new login gets a generated password,
// answered once (or the one in the body).
func (d *Daemon) createSubscription(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	email := r.PathValue("email")
	if email == "" {
		http.Error(w, "an email is required", http.StatusBadRequest)
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	if body.User != "" && !subauth.ValidUser(body.User) {
		http.Error(w, subauth.ErrUser.Error(), http.StatusBadRequest)
		return
	}
	existing, have := d.subByEmail(email)
	pass := ""
	switch {
	case body.Password != "":
		h, err := subauth.Password(body.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pass = h
	case have && existing.PassHash != "":
		// keep the current password
	default:
		gen, err := subauth.NewPassword()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h, err := subauth.Password(gen)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pass = h
		body.Password = gen
	}
	token, err := d.subToken(r.Context(), m, email, body.User, pass)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rec, _ := subauth.Get(d.store.State, token)
	out := map[string]any{"token": token, "email": email, "user": rec.User, "url": "/sub/" + token}
	if body.Password != "" {
		out["password"] = body.Password // shown once
	}
	apiserver.JSON(w, http.StatusOK, out)
}

// resetSubPassword regenerates a subscription's password and answers it once.
func (d *Daemon) resetSubPassword(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	email := r.PathValue("email")
	rec, ok := d.subByEmail(email)
	if !ok {
		http.Error(w, subauth.ErrNoSubscription.Error(), http.StatusNotFound)
		return
	}
	gen, err := subauth.NewPassword()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h, err := subauth.Password(gen)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := m.Set(r.Context(), subauth.Table, rec.Token, map[string]any{"pass": h}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{"token": rec.Token, "user": rec.User, "password": gen})
}

// subByEmail finds the subscription token for an email.
func (d *Daemon) subByEmail(email string) (subauth.Record, bool) {
	for _, token := range d.store.State.Keys(subauth.Table) {
		rec, ok := subauth.Get(d.store.State, token)
		if ok && rec.Email == email {
			return rec, true
		}
	}
	return subauth.Record{}, false
}

// subLogin checks a subscription login and sets its management cookie.
func (d *Daemon) subLogin(w http.ResponseWriter, r *http.Request) {
	h := d.host.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		apiserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	rec, err := subauth.Login(d.store.State, body.User, body.Password)
	if err != nil {
		apiserver.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	cookie, expires, err := subauth.NewSession(r.Context(), h, rec.Token)
	if err != nil {
		apiserver.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: subauth.CookieName, Value: cookie, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, Expires: expires,
	})
	apiserver.JSON(w, http.StatusOK, map[string]any{"user": rec.User, "email": rec.Email, "expires": expires.UnixMilli()})
}

// subSession answers the subscription behind the management cookie.
func (d *Daemon) subSession(r *http.Request) (subauth.Record, bool) {
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	c, err := r.Cookie(subauth.CookieName)
	if err != nil || c.Value == "" {
		return subauth.Record{}, false
	}
	return subauth.FromCookie(d.store.State, c.Value, time.Now())
}

// mountSubAPI is the subscription's management surface, under /api/v1/sub. It
// is registered before the catch-all so its more specific patterns win.
func (d *Daemon) mountSubAPI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/sub/login", d.subLogin)
	mux.HandleFunc("POST /api/v1/sub/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(subauth.CookieName); err == nil && c.Value != "" {
			if h := d.host.Load(); h != nil {
				_ = subauth.EndSession(r.Context(), h, c.Value)
			}
		}
		http.SetCookie(w, &http.Cookie{Name: subauth.CookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/sub/me", d.subMe)
	mux.HandleFunc("GET /api/v1/sub/vnet", d.subVnet)
	mux.HandleFunc("POST /api/v1/sub/vnet", d.subVnetCreate)
	mux.HandleFunc("POST /api/v1/sub/vnet/invites", d.subVnetInvite)
	mux.HandleFunc("POST /api/v1/sub/vnet/join", d.subVnetJoin)
	mux.HandleFunc("POST /api/v1/sub/vnet/leave", d.subVnetLeave)
}

// subMe is the subscription's own view: who they are, their links, traffic
// and expiry, and their virtual network.
func (d *Daemon) subMe(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	rendered := d.renderSub(rec, rec.Token, r)
	meta := map[string]any{
		"usedBytes": rendered.info.Quota.Used(), "totalBytes": rendered.info.Quota.Total,
		"uploadBytes": rendered.info.Quota.Upload, "downloadBytes": rendered.info.Quota.Download,
		"expire": rendered.info.Quota.Expire, "title": rendered.info.Title,
		"announce": rendered.info.Announce.Text, "supportUrl": rendered.info.SupportURL,
	}
	servers := make([]map[string]any, 0, len(rendered.info.Servers))
	for _, name := range submeta.SortedRemarks(rendered.info.Servers) {
		q := rendered.info.Servers[name]
		servers = append(servers, map[string]any{"name": name, "upload": q.Upload, "download": q.Download})
	}
	meta["servers"] = servers
	out := map[string]any{
		"user": rec.User, "email": rec.Email, "links": rendered.links, "meta": meta,
	}
	if st := d.vnets.Load(); st != nil {
		out["vnet"] = d.vnetView(r, st, rec.Email)
	}
	apiserver.JSON(w, http.StatusOK, out)
}

// vnetView answers a user's network and peers, creating the network user on
// first look, so a subscription can use virtual networks without a separate
// account.
func (d *Daemon) vnetView(r *http.Request, st *vnet.Store, email string) map[string]any {
	if _, ok := st.User(email); !ok {
		if _, err := st.CreateUser(r.Context(), email); err != nil {
			return map[string]any{"error": err.Error()}
		}
	}
	u, _ := st.User(email)
	out := map[string]any{"user": u.Email, "ip": u.IP, "name": u.Name}
	if m, in := st.NetworkOf(email); in {
		online := d.presence.Where()
		var members []map[string]any
		for _, mm := range st.Members(m.Network) {
			peer, _ := st.User(mm.User)
			members = append(members, map[string]any{
				"id": mm.User, "name": peer.Name, "ip": peer.IP, "role": mm.Role, "node": online[peer.Email],
			})
		}
		out["network"] = m.Network
		out["role"] = m.Role
		out["members"] = members
	}
	return out
}

func (d *Daemon) subVnet(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	st := d.vnets.Load()
	if st == nil {
		apiserver.Error(w, http.StatusServiceUnavailable, "starting")
		return
	}
	apiserver.JSON(w, http.StatusOK, d.vnetView(r, st, rec.Email))
}

func (d *Daemon) subVnetCreate(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	st := d.vnets.Load()
	if st == nil {
		apiserver.Error(w, http.StatusServiceUnavailable, "starting")
		return
	}
	var body struct{ Title string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	if _, err := st.CreateUser(r.Context(), rec.Email); err != nil {
		apiserver.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, err := st.CreateNetwork(r.Context(), rec.Email, body.Title)
	if err != nil {
		apiserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{"network": id})
}

func (d *Daemon) subVnetInvite(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	st := d.vnets.Load()
	if st == nil {
		apiserver.Error(w, http.StatusServiceUnavailable, "starting")
		return
	}
	var body struct {
		TTL  string `json:"ttl"`
		Uses int    `json:"uses"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	ttl, err := time.ParseDuration(body.TTL)
	if err != nil || ttl <= 0 {
		ttl = 24 * time.Hour
	}
	code, err := st.Invite(r.Context(), rec.Email, ttl, body.Uses)
	if err != nil {
		apiserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{"code": code})
}

func (d *Daemon) subVnetJoin(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	st := d.vnets.Load()
	if st == nil {
		apiserver.Error(w, http.StatusServiceUnavailable, "starting")
		return
	}
	var body struct{ Code string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	if _, err := st.CreateUser(r.Context(), rec.Email); err != nil {
		apiserver.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := st.Join(r.Context(), rec.Email, body.Code); err != nil {
		apiserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (d *Daemon) subVnetLeave(w http.ResponseWriter, r *http.Request) {
	rec, ok := d.subSession(r)
	if !ok {
		apiserver.Error(w, http.StatusUnauthorized, "log in first")
		return
	}
	st := d.vnets.Load()
	if st == nil {
		apiserver.Error(w, http.StatusServiceUnavailable, "starting")
		return
	}
	if err := st.Leave(r.Context(), rec.Email); err != nil {
		apiserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// vnetAPI is the users' side of the network, under /api/v1/.
func (d *Daemon) vnetAPI(w http.ResponseWriter, r *http.Request) {
	s := d.vnets.Load()
	if s == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	vnet.API(s, d.presence.Where).ServeHTTP(w, r)
}

func (d *Daemon) Close() error {
	if d.api != nil {
		_ = d.api.Close()
	}
	d.bus.Close()
	return errors.Join(d.edge.Close())
}
