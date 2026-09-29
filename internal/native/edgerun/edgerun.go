// Package edgerun runs a node's 443: SNI routes splice TLS untouched, names
// the node terminates are served with the master's certificates and proxied
// by host and path (inbounds, subscriptions, sites).
package edgerun

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/edge"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

type Runner struct {
	Listen string // ":443"
	Logf   func(format string, args ...any)

	mu      sync.Mutex
	started bool
	ln      net.Listener
	router  *edge.Router
	httpr   *edge.HTTPRouter
	handoff *edge.HandoffListener
	srv     *http.Server
	certs   atomic.Pointer[certTable]
	Drain   *edge.Drain
}

type certTable struct {
	exact    map[string]*tls.Certificate
	wildcard map[string]*tls.Certificate // zone -> cert for *.zone
}

func (t *certTable) pick(name string) *tls.Certificate {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if c := t.exact[name]; c != nil {
		return c
	}
	if _, zone, ok := strings.Cut(name, "."); ok {
		return t.wildcard[zone]
	}
	return nil
}

func loadCerts(certs []nodespec.Cert) (*certTable, error) {
	t := &certTable{exact: map[string]*tls.Certificate{}, wildcard: map[string]*tls.Certificate{}}
	for _, c := range certs {
		pair, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
		if err != nil {
			return nil, fmt.Errorf("edgerun: certificate for %v: %w", c.Names, err)
		}
		for _, n := range c.Names {
			n = strings.ToLower(n)
			if zone, ok := strings.CutPrefix(n, "*."); ok {
				t.wildcard[zone] = &pair
			} else {
				t.exact[n] = &pair
			}
		}
	}
	return t, nil
}

// Apply swaps in a new table and certificates; anything invalid is
// refused before it replaces what runs. The first Apply starts listening.
func (r *Runner) Apply(e nodespec.Edge, certs []nodespec.Cert) error {
	table, err := loadCerts(certs)
	if err != nil {
		return err
	}
	cfg := edge.Config{Default: edge.Route{Action: edge.ActionReject}}
	for _, rt := range e.SNI {
		action := edge.ActionTCP
		if rt.Action == "handoff" {
			action = edge.ActionHandoff
			if table.pick(rt.Match) == nil {
				return fmt.Errorf("edgerun: no certificate for %s", rt.Match)
			}
		}
		cfg.Routes = append(cfg.Routes, edge.Route{Match: rt.Match, Action: action, Target: rt.Target, ProxyProtocol: rt.ProxyProtocol})
	}
	if err := cfg.Validate([]string{nodespec.Handoff}); err != nil {
		return fmt.Errorf("edgerun: %w", err)
	}
	routes := make([]edge.HTTPRoute, 0, len(e.HTTP))
	for _, h := range e.HTTP {
		routes = append(routes, edge.HTTPRoute{Host: h.Host, PathPrefix: h.PathPrefix, Target: h.Target})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		if err := r.start(); err != nil {
			return err
		}
	}
	if err := r.httpr.Update(routes); err != nil {
		return fmt.Errorf("edgerun: %w", err)
	}
	r.certs.Store(table)
	r.router.Update(cfg)
	return nil
}

func (r *Runner) start() error {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", r.Listen)
	if err != nil {
		return err
	}
	if r.Drain == nil {
		r.Drain = edge.NewDrain()
	}
	r.handoff = edge.NewHandoffListener(ln.Addr())
	r.router = edge.NewRouter(edge.Config{Default: edge.Route{Action: edge.ActionReject}},
		map[string]*edge.HandoffListener{nodespec.Handoff: r.handoff})
	r.router.Drain, r.router.Logf = r.Drain, r.Logf
	r.httpr = edge.NewHTTPRouter(http.NotFoundHandler(), r.Drain)
	r.srv = &http.Server{
		Handler:           r.httpr,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// WebSocket upgrades through the reverse proxy need HTTP/1.1
			NextProtos: []string{"http/1.1"},
			GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
				if c := r.certs.Load().pick(h.ServerName); c != nil {
					return c, nil
				}
				return nil, errors.New("no certificate for " + h.ServerName)
			},
		},
	}
	r.certs.Store(&certTable{})
	r.ln, r.started = ln, true
	go func() { _ = r.router.Serve(ln) }()
	go func() { _ = r.srv.ServeTLS(r.handoff, "", "") }()
	return nil
}

// SetDrain turns maintenance on or off; idle "" keeps the edge default.
func (r *Runner) SetDrain(d nodespec.Drain) error {
	idle := time.Duration(0)
	if d.Idle != "" {
		v, err := time.ParseDuration(d.Idle)
		if err != nil {
			return fmt.Errorf("edgerun: drain idle: %w", err)
		}
		idle = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Drain == nil {
		r.Drain = edge.NewDrain()
	}
	r.Drain.Set(d.On, idle)
	return nil
}

// DrainState is the maintenance state and the tunnels still open.
func (r *Runner) DrainState() edge.DrainState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Drain.State()
}

// Addr is where the edge listens, once started.
func (r *Runner) Addr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return nil
	}
	return r.ln.Addr()
}

func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return nil
	}
	r.started = false
	return errors.Join(r.ln.Close(), r.handoff.Close(), r.srv.Close())
}
