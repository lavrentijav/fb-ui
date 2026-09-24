package certissuer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	propagationTimeout = 3 * time.Minute
	propagationPoll    = 5 * time.Second
)

// challengeName is where a dns-01 token lives; a wildcard and its apex share
// one name, which is why values are grouped per name before writing.
func challengeName(domain string) string {
	return "_acme-challenge." + strings.TrimPrefix(domain, "*.")
}

func presentDNS01(ctx context.Context, client *acme.Client, req *Request, pending []pendingChallenge) (func(), error) {
	byName := map[string][]string{}
	var names []string
	for _, p := range pending {
		v, err := client.DNS01ChallengeRecord(p.chal.Token)
		if err != nil {
			return func() {}, err
		}
		n := challengeName(p.domain)
		if _, seen := byName[n]; !seen {
			names = append(names, n)
		}
		byName[n] = append(byName[n], v)
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, n := range names {
			if err := req.DNS.DeleteRecords(cctx, n, "TXT"); err != nil {
				req.logf("cleanup of %s failed: %v", n, err)
			}
		}
	}
	for _, n := range names {
		if err := req.DNS.SetRecords(ctx, n, "TXT", byName[n], 60); err != nil {
			return cleanup, fmt.Errorf("dns01: write %s: %w", n, err)
		}
		req.logf("TXT %s published", n)
	}
	check := req.Propagated
	if check == nil {
		check = authoritativePropagated
	}
	for _, n := range names {
		if err := check(ctx, req.Zone, n, byName[n]); err != nil {
			return cleanup, err
		}
	}
	return cleanup, nil
}

// authoritativePropagated asks the zone's own nameservers, bypassing caches
// that would otherwise hold the NXDOMAIN seen before the record existed.
func authoritativePropagated(ctx context.Context, zone, name string, values []string) error {
	servers, err := zoneNameservers(ctx, zone)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(propagationTimeout)
	for {
		missing := ""
		for _, ns := range servers {
			got, err := lookupTXTAt(ctx, ns, name)
			if err != nil || !containsAll(got, values) {
				missing = ns
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dns01: %s not visible at %s after %s", name, missing, propagationTimeout)
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
		return nil, fmt.Errorf("dns01: NS lookup for %s: %w", zone, err)
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
		return nil, fmt.Errorf("dns01: no reachable nameserver for %s", zone)
	}
	return out, nil
}

func lookupTXTAt(ctx context.Context, server, name string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
	return r.LookupTXT(ctx, name)
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// presentHTTP01 serves tokens from a short-lived listener. A busy port is the
// usual failure (nginx fronting a fallback site), so it gets a clear message.
func presentHTTP01(ctx context.Context, client *acme.Client, req *Request, pending []pendingChallenge) (func(), error) {
	addr := req.HTTPAddr
	if addr == "" {
		addr = ":80"
	}
	var mu sync.Mutex
	tokens := map[string]string{}
	for _, p := range pending {
		body, err := client.HTTP01ChallengeResponse(p.chal.Token)
		if err != nil {
			return func() {}, err
		}
		tokens[client.HTTP01ChallengePath(p.chal.Token)] = body
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return func() {}, fmt.Errorf("http01: cannot listen on %s (is nginx or another site on it? use dns01): %w", addr, err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			body, ok := tokens[r.URL.Path]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(body))
		}),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			req.logf("http01 listener stopped: %v", err)
		}
	}()
	req.logf("http01 listener on %s", ln.Addr())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
