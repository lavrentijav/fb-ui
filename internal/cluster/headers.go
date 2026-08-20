package cluster

import (
	"net"
	"strings"
)

// Header names carrying the fallback endpoints a client should retry when its
// primary subscription host stops answering.
const (
	HeaderFallbackDomains = "X-Subscription-Fallback-Domains"
	HeaderFallbackIPs     = "X-Subscription-Fallback-IPs"
	HeaderSignature       = "X-Subscription-Signature"
)

// Caps keep a large peer set from producing a header some proxy will truncate.
// With the 253-byte host limit below the worst case stays near 2 KiB, well
// inside the 8 KiB budget proxies commonly allow for all headers together.
const (
	MaxFallbackDomains = 8
	MaxFallbackIPs     = 16
)

// Endpoint is one advertisable subscription host. It is deliberately not the
// DB model so this package stays free of database imports.
type Endpoint struct {
	Domain string
	Ips    []string
}

// FallbackDomains renders the domain header value: deduplicated, input order
// preserved, capped at MaxFallbackDomains.
func FallbackDomains(peers []Endpoint) string {
	values := make([]string, 0, len(peers))
	for _, p := range peers {
		if d := sanitizeDomain(p.Domain); d != "" {
			values = append(values, d)
		}
	}
	return joinCapped(values, MaxFallbackDomains)
}

// FallbackIPs renders the IP header value. Entries that are not parseable IP
// literals are dropped rather than passed through, so a typo cannot smuggle a
// hostname — or a header separator — into the list.
func FallbackIPs(peers []Endpoint) string {
	values := make([]string, 0, len(peers)*2)
	for _, p := range peers {
		for _, raw := range p.Ips {
			ip := net.ParseIP(strings.TrimSpace(raw))
			if ip == nil {
				continue
			}
			values = append(values, ip.String())
		}
	}
	return joinCapped(values, MaxFallbackIPs)
}

// sanitizeDomain accepts a plain host name. Anything carrying a separator,
// whitespace or a control character is rejected outright: these values end up
// in a response header, where a stray CR or LF would be a header injection.
func sanitizeDomain(raw string) string {
	d := strings.TrimSpace(raw)
	if d == "" || len(d) > 253 {
		return ""
	}
	for _, r := range d {
		if r <= ' ' || r == ',' || r == ';' || r == 0x7f {
			return ""
		}
	}
	return d
}

func joinCapped(values []string, maxCount int) string {
	var b strings.Builder
	seen := make(map[string]struct{}, len(values))
	count := 0
	for _, v := range values {
		if _, dup := seen[v]; dup {
			continue
		}
		if count == maxCount {
			break
		}
		if count > 0 {
			b.WriteString(", ")
		}
		b.WriteString(v)
		seen[v] = struct{}{}
		count++
	}
	return b.String()
}
