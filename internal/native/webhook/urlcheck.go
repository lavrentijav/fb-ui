package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// LookupIP resolves a host name; tests replace it.
var LookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, err
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func blocked(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a) || (a.Is4() && a.As4()[0] == 0)
}

// CheckPublicURL accepts an http(s) address whose host is public: a literal
// address must not be private, and a name must resolve only to public
// addresses. It is checked when a webhook is saved and again on every send,
// because DNS may have moved since.
func CheckPublicURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("an http or https address is required")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if blocked(ip) {
			return fmt.Errorf("%s is a private address", host)
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return errors.New("localhost is a private address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := LookupIP(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%s does not resolve", host)
	}
	for _, ip := range ips {
		if blocked(ip) {
			return fmt.Errorf("%s resolves to a private address", host)
		}
	}
	return nil
}
