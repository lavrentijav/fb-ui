package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Route actions.
const (
	ActionHandoff = "handoff" // give the connection to an in-process server
	ActionTCP     = "tcp"     // splice to Target, TLS untouched
	ActionReject  = "reject"
)

// Route sends TLS connections whose SNI matches Match somewhere. Match is an
// exact name, "*.zone" for any name below zone, or "" for no SNI at all.
type Route struct {
	Match         string
	Action        string
	Target        string // tcp: host:port; handoff: the handoff name
	ProxyProtocol int    // 0 none, 1 or 2: PROXY header version sent to Target
}

// Config is one complete, immutable routing table.
type Config struct {
	Routes []Route
	// Default takes every connection no route matches, including non-TLS.
	Default Route
}

// Router accepts connections on one port and dispatches them by SNI.
type Router struct {
	cfg      atomic.Pointer[Config]
	handoffs map[string]*HandoffListener

	HelloTimeout time.Duration
	DialTimeout  time.Duration
	IdleTimeout  time.Duration
	Logf         func(format string, args ...any)
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	// Drain, when set, counts spliced tunnels and refuses new ones while on.
	Drain *Drain

	wg sync.WaitGroup
}

// NewRouter builds a router; handoffs are the in-process servers a route may
// name, such as "panel".
func NewRouter(cfg Config, handoffs map[string]*HandoffListener) *Router {
	r := &Router{
		handoffs:     handoffs,
		HelloTimeout: 10 * time.Second,
		DialTimeout:  10 * time.Second,
		IdleTimeout:  5 * time.Minute,
	}
	r.cfg.Store(&cfg)
	return r
}

// Update swaps the routing table; connections already spliced are unaffected.
func (r *Router) Update(cfg Config) { r.cfg.Store(&cfg) }

// Config returns the table in force.
func (r *Router) Config() Config { return *r.cfg.Load() }

func (r *Router) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Serve accepts until ln is closed.
func (r *Router) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				r.wg.Wait()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			r.wg.Wait()
			return err
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c)
		}()
	}
}

// Pick returns the route for a hello; nil hello means the client did not
// speak TLS.
func (cfg *Config) Pick(h *Hello) Route {
	if h == nil {
		return cfg.Default
	}
	sni := strings.ToLower(strings.TrimSuffix(h.SNI, "."))
	best, bestLen := -1, -1
	for i, rt := range cfg.Routes {
		if n := matchLen(rt.Match, sni); n > bestLen {
			best, bestLen = i, n
		}
	}
	if best < 0 {
		return cfg.Default
	}
	return cfg.Routes[best]
}

// matchLen scores a pattern against sni: -1 no match, longer is more
// specific, and an exact name beats any wildcard covering it.
func matchLen(pattern, sni string) int {
	pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	switch {
	case pattern == sni:
		return 1<<16 + len(pattern)
	case strings.HasPrefix(pattern, "*.") && sni != "" && strings.HasSuffix(sni, pattern[1:]):
		return len(pattern)
	}
	return -1
}

func (r *Router) handle(c net.Conn) {
	hello, raw, err := readHello(c, c.SetReadDeadline, r.HelloTimeout)
	if err != nil && !errors.Is(err, errNotTLS) {
		_ = c.Close()
		return
	}
	cfg := r.cfg.Load()
	rt := cfg.Pick(hello)
	sc := &sniffedConn{Conn: c, pending: raw}
	switch rt.Action {
	case ActionHandoff:
		hl := r.handoffs[rt.Target]
		if hl == nil || !hl.deliver(sc) {
			r.logf("edge: no in-process server %q for %s", rt.Target, describe(hello))
			_ = c.Close()
		}
	case ActionTCP:
		if r.Drain.Draining() {
			_ = c.Close()
			return
		}
		r.splice(sc, rt, hello)
	default:
		_ = c.Close()
	}
}

func describe(h *Hello) string {
	if h == nil {
		return "a non-TLS client"
	}
	if h.SNI == "" {
		return "a client without SNI"
	}
	return "SNI " + h.SNI
}

func (r *Router) splice(c *sniffedConn, rt Route, h *Hello) {
	defer c.Close()
	dial := r.Dial
	if dial == nil {
		d := &net.Dialer{}
		dial = d.DialContext
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.DialTimeout)
	up, err := dial(ctx, "tcp", rt.Target)
	cancel()
	if err != nil {
		r.logf("edge: %s -> %s: %v", describe(h), rt.Target, err)
		return
	}
	up = r.Drain.Track(up, c)
	defer up.Close()
	if rt.ProxyProtocol > 0 {
		if _, err := up.Write(proxyHeader(rt.ProxyProtocol, c.RemoteAddr(), c.LocalAddr())); err != nil {
			return
		}
	}
	pipe(c, up, r.IdleTimeout)
}

// pipe copies both ways and propagates half-closes, so a peer that shuts its
// write side still receives the rest of the response.
func pipe(a, b net.Conn, idle time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, &idleReader{Conn: src, idle: idle})
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

type idleReader struct {
	net.Conn
	idle time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	if r.idle > 0 {
		_ = r.SetReadDeadline(time.Now().Add(r.idle))
	}
	return r.Conn.Read(p)
}

// sniffedConn replays the bytes consumed while reading the ClientHello.
type sniffedConn struct {
	net.Conn
	mu      sync.Mutex
	pending []byte
}

func (c *sniffedConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}

// CloseWrite reaches the TCP connection underneath for half-closes.
func (c *sniffedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// HandoffListener is a net.Listener fed by the router, so an in-process
// server such as the panel sees the client's real address.
type HandoffListener struct {
	addr   net.Addr
	conns  chan net.Conn
	once   sync.Once
	closed chan struct{}
}

func NewHandoffListener(addr net.Addr) *HandoffListener {
	return &HandoffListener{addr: addr, conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *HandoffListener) deliver(c net.Conn) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.closed:
		return false
	case <-time.After(10 * time.Second):
		return false
	}
}

func (l *HandoffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *HandoffListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *HandoffListener) Addr() net.Addr { return l.addr }

// Validate reports the first route that could not work, so a bad table is
// refused before it replaces a good one.
func (cfg *Config) Validate(handoffs []string) error {
	check := func(name string, rt Route) error {
		switch rt.Action {
		case ActionReject:
		case ActionHandoff:
			for _, h := range handoffs {
				if h == rt.Target {
					return nil
				}
			}
			return fmt.Errorf("%s: unknown in-process server %q", name, rt.Target)
		case ActionTCP:
			host, port, err := net.SplitHostPort(rt.Target)
			if err != nil || host == "" || port == "" {
				return fmt.Errorf("%s: target %q is not host:port", name, rt.Target)
			}
		default:
			return fmt.Errorf("%s: unknown action %q", name, rt.Action)
		}
		if rt.ProxyProtocol < 0 || rt.ProxyProtocol > 2 {
			return fmt.Errorf("%s: PROXY protocol version must be 0, 1 or 2", name)
		}
		return nil
	}
	seen := map[string]bool{}
	for _, rt := range cfg.Routes {
		m := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rt.Match), "."))
		if strings.Contains(strings.TrimPrefix(m, "*."), "*") {
			return fmt.Errorf("route %q: only a leading *. wildcard is supported", rt.Match)
		}
		if seen[m] {
			return fmt.Errorf("route %q is listed twice", rt.Match)
		}
		seen[m] = true
		if err := check("route "+rt.Match, rt); err != nil {
			return err
		}
	}
	return check("default route", cfg.Default)
}
