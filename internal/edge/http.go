package edge

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// HTTPRoute proxies requests whose Host and path match to Target, an
// http(s) URL of a local service: a WebSocket or XHTTP inbound, a site, the
// subscription server. Host is exact, "*.zone" or "" for any.
type HTTPRoute struct {
	Host        string
	PathPrefix  string
	Target      string
	StripPrefix bool
}

type compiledRoute struct {
	HTTPRoute
	proxy *httputil.ReverseProxy
}

// HTTPRouter serves matching requests itself and passes the rest to Next,
// which is the panel.
type HTTPRouter struct {
	routes atomic.Pointer[[]compiledRoute]
	Next   http.Handler
}

func NewHTTPRouter(next http.Handler) *HTTPRouter {
	h := &HTTPRouter{Next: next}
	h.routes.Store(&[]compiledRoute{})
	return h
}

// Update validates and swaps the routes; on error the old ones stay.
func (h *HTTPRouter) Update(routes []HTTPRoute) error {
	out := make([]compiledRoute, 0, len(routes))
	for _, rt := range routes {
		c, err := compile(rt)
		if err != nil {
			return err
		}
		out = append(out, c)
	}
	h.routes.Store(&out)
	return nil
}

func compile(rt HTTPRoute) (compiledRoute, error) {
	if !strings.HasPrefix(rt.PathPrefix, "/") {
		return compiledRoute{}, fmt.Errorf("path %q must start with /", rt.PathPrefix)
	}
	if strings.Contains(strings.TrimPrefix(rt.Host, "*."), "*") {
		return compiledRoute{}, fmt.Errorf("host %q: only a leading *. wildcard is supported", rt.Host)
	}
	u, err := url.Parse(rt.Target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return compiledRoute{}, fmt.Errorf("target %q must be an http(s) URL", rt.Target)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == "https" && isLoopback(u.Hostname()) {
		// A local backend serves the public name's certificate, which does
		// not match 127.0.0.1; the hop never leaves the machine.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	prefix := strings.TrimSuffix(rt.PathPrefix, "/")
	proxy := &httputil.ReverseProxy{
		Transport: tr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
			if rt.StripPrefix && prefix != "" {
				p := strings.TrimPrefix(pr.In.URL.Path, prefix)
				if !strings.HasPrefix(p, "/") {
					p = "/" + p
				}
				pr.Out.URL.Path = singleJoin(u.Path, p)
				pr.Out.URL.RawPath = ""
			}
		},
	}
	return compiledRoute{HTTPRoute: rt, proxy: proxy}, nil
}

func singleJoin(a, b string) string {
	if a == "" || a == "/" {
		return b
	}
	return strings.TrimSuffix(a, "/") + b
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pathMatches keeps /ws from also catching /wsx: a prefix must end at a
// segment boundary unless it already ends with a slash.
func pathMatches(prefix, path string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return len(path) == len(prefix) || strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/'
}

func (h *HTTPRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	var best *compiledRoute
	bestHost, bestPath := -2, -1
	routes := *h.routes.Load()
	for i := range routes {
		rt := &routes[i]
		hs := 0
		if rt.Host != "" {
			if hs = matchLen(rt.Host, host); hs < 0 {
				continue
			}
		}
		if !pathMatches(rt.PathPrefix, r.URL.Path) {
			continue
		}
		if hs > bestHost || (hs == bestHost && len(rt.PathPrefix) > bestPath) {
			best, bestHost, bestPath = rt, hs, len(rt.PathPrefix)
		}
	}
	if best == nil {
		h.Next.ServeHTTP(w, r)
		return
	}
	// The panel's short server timeouts suit its API, not a tunnel that
	// streams for hours.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	best.proxy.ServeHTTP(w, r)
}
