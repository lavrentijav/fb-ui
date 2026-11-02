package masterd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// DefaultAdminListen keeps the administrative surface on loopback: the panel
// (which knows the session cookie) is the only intended client, and scripts on
// the same host can use an API token. Port 0 asks the kernel for a free port,
// which is logged at startup.
const DefaultAdminListen = "127.0.0.1:0"

// AdminHandler is Handler behind authentication. Everything is closed except
// the paths that carry their own credential or are public by design:
//
//   - /healthz and /ready: liveness, no data,
//   - /enroll: the node redeems a one-time token,
//   - /sub/*: the subscription token is in the path,
//   - /panel/*: the built interface (its /panel/api half authenticates itself
//     through the module API).
//
// A caller passes either an API token (Authorization: Bearer) or the panel's
// session cookie.
func (d *Daemon) AdminHandler() http.Handler {
	// The host appears only once the master is ready, while the listener may
	// already be open; resolve it on the first request past readiness and keep
	// the guarded handler. Before that every request is refused, never served
	// unguarded.
	var guarded atomic.Value
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := guarded.Load().(http.Handler); ok {
			h.ServeHTTP(w, r)
			return
		}
		host := d.host.Load()
		if host == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		var session module.SessionAuth
		for _, m := range module.All() {
			if sa, ok := m.(module.SessionAuth); ok {
				session = sa
			}
		}
		h := apiserver.RequireAuth(host, session, PublicAdminPath, d.Handler())
		guarded.Store(h)
		h.ServeHTTP(w, r)
	})
}

// PublicAdminPath says whether an admin-surface request needs no credential.
func PublicAdminPath(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/healthz", p == "/ready":
		return true
	case p == "/enroll", strings.HasPrefix(p, "/enroll/"):
		return true
	case strings.HasPrefix(p, "/sub/"):
		return true
	case p == "/panel", strings.HasPrefix(p, "/panel/"):
		return true
	}
	return false
}

// AdminAddress resolves the configured admin listener, applying the loopback
// default.
func (d *Daemon) AdminAddress() (string, error) {
	addr := strings.TrimSpace(d.cfg.AdminListen)
	if addr == "" {
		addr = DefaultAdminListen
	}
	if !d.cfg.AdminAllowPublic && !loopbackHost(addr) {
		return "", errors.New("admin: " + addr + " is not a loopback address; set AdminAllowPublic to expose it deliberately")
	}
	return addr, nil
}

// loopbackHost reports whether addr binds only the loopback interface. An
// empty host (":8080") binds every interface and is not loopback.
func loopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListenAdmin serves the authenticated admin surface until ctx ends. It is the
// production path for the network entrypoint; handlers that run in-process
// (the lab, tests) use Handler directly.
func (d *Daemon) ListenAdmin(ctx context.Context) error {
	addr, err := d.AdminAddress()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: d.AdminHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	d.logf("admin api on http://%s", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
