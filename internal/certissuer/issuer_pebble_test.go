package certissuer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive a real ACME order against Pebble. Start it with
// pebble-challtestsrv as its DNS (see pebbleEnv) or they skip.
type pebbleEnv struct {
	directory string
	challsrv  string
	client    *http.Client
}

func requirePebble(t *testing.T) pebbleEnv {
	t.Helper()
	dir, chall, ca := os.Getenv("PEBBLE_DIRECTORY"), os.Getenv("PEBBLE_CHALLTESTSRV"), os.Getenv("PEBBLE_CA_FILE")
	if dir == "" || chall == "" || ca == "" {
		t.Skip("PEBBLE_DIRECTORY, PEBBLE_CHALLTESTSRV and PEBBLE_CA_FILE not set")
	}
	caPEM, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("PEBBLE_CA_FILE holds no certificate")
	}
	return pebbleEnv{
		directory: dir,
		challsrv:  strings.TrimRight(chall, "/"),
		client:    &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}
}

// challtestProvider is a dnsprovider.Provider backed by challtestsrv's
// management API, so Pebble validates against records we really wrote.
type challtestProvider struct {
	base string
	mu   sync.Mutex
	txt  map[string][]string
}

func (p *challtestProvider) post(path string, body map[string]string) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(p.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func (p *challtestProvider) SetRecords(_ context.Context, name, rtype string, values []string, _ int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	host := strings.TrimSuffix(name, ".") + "."
	if err := p.post("/clear-txt", map[string]string{"host": host}); err != nil {
		return err
	}
	for _, v := range values {
		if err := p.post("/set-txt", map[string]string{"host": host, "value": v}); err != nil {
			return err
		}
	}
	if p.txt == nil {
		p.txt = map[string][]string{}
	}
	p.txt[name] = slices.Clone(values)
	return nil
}

func (p *challtestProvider) DeleteRecords(ctx context.Context, name, rtype string) error {
	return p.SetRecords(ctx, name, rtype, nil, 0)
}

func (p *challtestProvider) Check(context.Context) error { return nil }

func TestIssueAgainstPebble(t *testing.T) {
	env := requirePebble(t)
	key, _, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("dns01 wildcard and apex share one TXT name", func(t *testing.T) {
		dns := &challtestProvider{base: env.challsrv}
		var propagatedNames []string
		res, err := Issue(context.Background(), Request{
			DirectoryURL: env.directory,
			AccountKey:   key,
			Email:        "ops@example.com",
			Domains:      []string{"Node1.Example.com", "*.node1.example.com"},
			Challenge:    DNS01,
			DNS:          dns,
			Zone:         "example.com",
			HTTPClient:   env.client,
			Propagated: func(_ context.Context, _, name string, values []string) error {
				propagatedNames = append(propagatedNames, name)
				if len(values) != 2 {
					t.Errorf("%s published %d values, want 2 (apex + wildcard)", name, len(values))
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if want := []string{"_acme-challenge.node1.example.com"}; !slices.Equal(propagatedNames, want) {
			t.Fatalf("propagated names = %v, want %v", propagatedNames, want)
		}
		leaf := parseLeaf(t, res.CertPEM)
		if !slices.Equal(leaf.DNSNames, []string{"node1.example.com", "*.node1.example.com"}) {
			t.Fatalf("SANs = %v", leaf.DNSNames)
		}
		if _, err := tls.X509KeyPair(res.CertPEM, res.KeyPEM); err != nil {
			t.Fatalf("cert and key do not pair: %v", err)
		}
		if got := dns.txt["_acme-challenge.node1.example.com"]; len(got) != 0 {
			t.Fatalf("challenge TXT left behind: %v", got)
		}
	})

	t.Run("http01 on a spare port", func(t *testing.T) {
		res, err := Issue(context.Background(), Request{
			DirectoryURL: env.directory,
			AccountKey:   key,
			Domains:      []string{"web.example.com"},
			Challenge:    HTTP01,
			HTTPAddr:     "127.0.0.1:5002",
			HTTPClient:   env.client,
		})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if leaf := parseLeaf(t, res.CertPEM); !slices.Equal(leaf.DNSNames, []string{"web.example.com"}) || res.NotAfter.Before(time.Now()) {
			t.Fatalf("leaf SANs=%v notAfter=%v", leaf.DNSNames, res.NotAfter)
		}
	})
}

func parseLeaf(t *testing.T, chain []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(chain)
	if b == nil {
		t.Fatal("no PEM in chain")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRequestValidate(t *testing.T) {
	key, _, _ := NewAccountKey()
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"wildcard over http01", Request{AccountKey: key, Domains: []string{"*.a.example.com"}, Challenge: HTTP01}, "wildcard *.a.example.com needs dns01"},
		{"dns01 without provider", Request{AccountKey: key, Domains: []string{"a.example.com"}, Challenge: DNS01}, "dns01 needs a DNS provider"},
		{"domain outside zone", Request{AccountKey: key, Domains: []string{"a.other.org"}, Challenge: DNS01, DNS: &challtestProvider{}, Zone: "example.com"}, "a.other.org is not in zone example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Issue(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
