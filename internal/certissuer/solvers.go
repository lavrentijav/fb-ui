package certissuer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/web/network"
)

const propagationTimeout = 3 * time.Minute

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
	type target struct {
		dns  dnsprovider.Provider
		zone string
	}
	targets := map[string]target{}
	var written []string
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, n := range written {
			if err := targets[n].dns.DeleteRecords(cctx, n, "TXT"); err != nil {
				req.logf("cleanup of %s failed: %v", n, err)
			}
		}
	}
	for _, n := range names {
		dns, zone, err := req.dnsFor(strings.TrimPrefix(n, "_acme-challenge."))
		if err != nil {
			return cleanup, err
		}
		targets[n] = target{dns, zone}
		if err := dns.SetRecords(ctx, n, "TXT", byName[n], 60); err != nil {
			return cleanup, fmt.Errorf("dns01: write %s: %w", n, err)
		}
		written = append(written, n)
		req.logf("TXT %s published", n)
	}
	check := req.Propagated
	if check == nil {
		check = authoritativePropagated
	}
	for _, n := range names {
		if err := check(ctx, targets[n].zone, n, byName[n]); err != nil {
			return cleanup, err
		}
	}
	return cleanup, nil
}

func authoritativePropagated(ctx context.Context, zone, name string, values []string) error {
	return dnsprovider.WaitVisible(ctx, zone, name, "TXT", values, propagationTimeout)
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
	go network.ServeHTTP(srv, ln, "acme http-01 listener")
	req.logf("http01 listener on %s", ln.Addr())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
