package dnsprovider

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

const propagationPoll = 5 * time.Second

// WaitVisible blocks until every value of name/rtype is served by all of the
// zone's own nameservers. Asking them directly skips resolver caches that
// would hold the NXDOMAIN seen before the record existed.
func WaitVisible(ctx context.Context, zone, name, rtype string, values []string, timeout time.Duration) error {
	servers, err := zoneNameservers(ctx, NormalizeName(zone))
	if err != nil {
		return err
	}
	rtype = strings.ToUpper(rtype)
	want := make([]string, 0, len(values))
	for _, v := range values {
		want = append(want, canonicalValue(rtype, v))
	}
	deadline := time.Now().Add(timeout)
	for {
		missing := ""
		for _, ns := range servers {
			got, err := lookupAt(ctx, ns, NormalizeName(name), rtype)
			if err != nil || !containsAll(got, want) {
				missing = ns
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dns: %s %s not visible at %s after %s", name, rtype, missing, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(propagationPoll):
		}
	}
}

func zoneNameservers(ctx context.Context, zone string) ([]string, error) {
	nss, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil {
		return nil, fmt.Errorf("dns: NS lookup for %s: %w", zone, err)
	}
	var out []string
	for _, ns := range nss {
		ips, err := net.DefaultResolver.LookupHost(ctx, ns.Host)
		if err != nil || len(ips) == 0 {
			continue
		}
		out = append(out, net.JoinHostPort(ips[0], "53"))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dns: no reachable nameserver for %s", zone)
	}
	return out, nil
}

func lookupAt(ctx context.Context, server, name, rtype string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
	var raw []string
	var err error
	switch rtype {
	case "TXT":
		raw, err = r.LookupTXT(ctx, name)
	case "A", "AAAA":
		network := "ip4"
		if rtype == "AAAA" {
			network = "ip6"
		}
		var ips []net.IP
		ips, err = r.LookupIP(ctx, network, name)
		for _, ip := range ips {
			raw = append(raw, ip.String())
		}
	default:
		return nil, fmt.Errorf("dns: cannot check %s records", rtype)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, canonicalValue(rtype, v))
	}
	return out, err
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
