package certissuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"slices"
	"testing"

	"golang.org/x/crypto/acme"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
)

type recordingDNS struct {
	name    string
	written []string
	deleted []string
}

func (r *recordingDNS) SetRecords(_ context.Context, name, _ string, _ []string, _ int) error {
	r.written = append(r.written, name)
	return nil
}

func (r *recordingDNS) DeleteRecords(_ context.Context, name, _ string) error {
	r.deleted = append(r.deleted, name)
	return nil
}
func (r *recordingDNS) Check(context.Context) error { return nil }

// A node named in two zones held by different accounts (Technitium and
// Cloudflare) needs each TXT token written, checked and removed in its own.
func TestDNS01SpansAccounts(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tech, cf := &recordingDNS{name: "tech"}, &recordingDNS{name: "cf"}
	var checked []string
	req := &Request{
		Domains: []string{"ru7.walk.boykisser.online", "ru7.walk.example.com"}, Challenge: DNS01, AccountKey: key,
		DNSFor: func(name string) (dnsprovider.Provider, string, error) {
			switch {
			case dnsprovider.InZone(name, "boykisser.online"):
				return tech, "boykisser.online", nil
			case dnsprovider.InZone(name, "example.com"):
				return cf, "example.com", nil
			}
			return nil, "", fmt.Errorf("no account for %s", name)
		},
		Propagated: func(_ context.Context, zone, name string, _ []string) error {
			checked = append(checked, zone+" "+name)
			return nil
		},
	}
	if err := req.validate(); err != nil {
		t.Fatal(err)
	}
	pending := []pendingChallenge{
		{domain: "ru7.walk.boykisser.online", chal: &acme.Challenge{Token: "t1"}},
		{domain: "ru7.walk.example.com", chal: &acme.Challenge{Token: "t2"}},
	}
	cleanup, err := presentDNS01(context.Background(), &acme.Client{Key: key}, req, pending)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if !slices.Equal(tech.written, []string{"_acme-challenge.ru7.walk.boykisser.online"}) ||
		!slices.Equal(cf.written, []string{"_acme-challenge.ru7.walk.example.com"}) ||
		!slices.Equal(cf.deleted, cf.written) || !slices.Equal(tech.deleted, tech.written) {
		t.Fatalf("tech=%+v cf=%+v", tech, cf)
	}
	if want := []string{"boykisser.online _acme-challenge.ru7.walk.boykisser.online", "example.com _acme-challenge.ru7.walk.example.com"}; !slices.Equal(checked, want) {
		t.Fatalf("propagation checked %v", checked)
	}
	req.Domains = append(req.Domains, "x.other.org")
	if err := req.validate(); err == nil {
		t.Fatal("a name no account holds must be refused before ordering")
	}
}
