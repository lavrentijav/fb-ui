package dnsacct

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"
)

// publicResolvers answer for delegation. The host's own resolver cannot: on a
// machine running its own authoritative server it returns that server's copy
// of a zone even when the internet is delegated elsewhere.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// lookupNS lists a zone's public name servers, lowercased and without the
// trailing dot; nil when no public resolver answers. Tests replace it.
var lookupNS = func(zone string) []string {
	for _, addr := range publicResolvers {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ns, err := r.LookupNS(ctx, zone)
		cancel()
		if err != nil || len(ns) == 0 {
			continue
		}
		out := make([]string, 0, len(ns))
		for _, n := range ns {
			out = append(out, strings.TrimSuffix(strings.ToLower(n.Host), "."))
		}
		return out
	}
	return nil
}

func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// delegatedTo says whether the zone's public delegation points at this
// account's servers; known is false when that cannot be told.
func delegatedTo(acc Account, zone string) (delegated, known bool) {
	hosts := lookupNS(zone)
	if len(hosts) == 0 {
		return false, false
	}
	switch acc.Type {
	case "cloudflare":
		for _, h := range hosts {
			if strings.HasSuffix(h, ".ns.cloudflare.com") {
				return true, true
			}
		}
		return false, true
	case "technitium":
		self := hostOf(acc.BaseURL)
		if self == "" {
			return false, false
		}
		for _, h := range hosts {
			if h == self {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}
