package masterjoin_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/masterjoin"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func parseLEAF(der []byte) (*x509.Certificate, error) { return x509.ParseCertificate(der) }

// hostWriter adapts the test host to masterjoin.Writer.
type hostWriter struct{ h *modtest.Host }

func (w hostWriter) Set(ctx context.Context, table, key string, fields map[string]any) error {
	_, err := w.h.Set(ctx, table, key, fields)
	return err
}
func (w hostWriter) Delete(ctx context.Context, table, key string) error {
	_, err := w.h.Delete(ctx, table, key)
	return err
}

func TestJoinFlowIssuesACertificateSignedByTheFleetCA(t *testing.T) {
	ca, err := fleetca.New("fleet")
	if err != nil {
		t.Fatal(err)
	}
	h := modtest.NewHost("fi1")
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	secret, err := masterjoin.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := masterjoin.Mint(ctx, hostWriter{h}, secret, "ru2", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	token := masterjoin.Token(secret, ca)
	if !strings.HasPrefix(token, masterjoin.TokenPrefix+"_") {
		t.Fatalf("token %q", token)
	}

	srv := httptest.NewServer(masterjoin.Handler(ca, hostWriter{h}, h, func() time.Time { return now }))
	defer srv.Close()
	res, err := masterjoin.Exchange(ctx, srv.URL, token, "ru2", []string{"ru2.m.example", "10.0.0.2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "ru2" || res.CA == "" || res.Cert == "" || res.Key == "" {
		t.Fatalf("result %+v", res)
	}
	// the certificate is usable with the CA as a mutual TLS pair
	pair, err := tls.X509KeyPair([]byte(res.Cert), []byte(res.Key))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = parseLEAF(pair.Certificate[0]); err != nil {
			t.Fatal(err)
		}
	}
	if pair.Leaf.Subject.CommonName != "ru2" {
		t.Fatalf("CN %q", pair.Leaf.Subject.CommonName)
	}
	if len(pair.Leaf.DNSNames) == 0 || pair.Leaf.DNSNames[0] != "ru2.m.example" {
		t.Fatalf("SANs %v", pair.Leaf.DNSNames)
	}
	// the token is one-time
	if _, err := masterjoin.Exchange(ctx, srv.URL, token, "ru2", nil, nil); !errors.Is(err, masterjoin.ErrUnknown) &&
		!strings.Contains(err.Error(), "unknown") {
		t.Fatalf("second redemption: %v", err)
	}
}

func TestJoinRefusesAWrongNameAndAnExpiredToken(t *testing.T) {
	ca, _ := fleetca.New("fleet")
	h := modtest.NewHost("fi1")
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	secret, _ := masterjoin.NewSecret()
	if err := masterjoin.Mint(ctx, hostWriter{h}, secret, "ru2", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	token := masterjoin.Token(secret, ca)
	srv := httptest.NewServer(masterjoin.Handler(ca, hostWriter{h}, h, func() time.Time { return now }))
	defer srv.Close()
	if _, err := masterjoin.Exchange(ctx, srv.URL, token, "de2", nil, nil); err == nil {
		t.Fatal("a token for ru2 joined as de2")
	}

	// an expired token is refused and removed
	secret2, _ := masterjoin.NewSecret()
	if err := masterjoin.Mint(ctx, hostWriter{h}, secret2, "de2", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	later := func() time.Time { return now.Add(2 * time.Minute) }
	srv2 := httptest.NewServer(masterjoin.Handler(ca, hostWriter{h}, h, later))
	defer srv2.Close()
	if _, err := masterjoin.Exchange(ctx, srv2.URL, masterjoin.Token(secret2, ca), "de2", nil, nil); err == nil {
		t.Fatal("an expired token was redeemed")
	}
	if _, ok := h.Get(masterjoin.Table, masterjoin.Hash(secret2)); ok {
		t.Fatal("the expired token was kept")
	}
}

func TestAJoinTokenCannotBeANodeToken(t *testing.T) {
	secret := "abc"
	if _, _, ok := masterjoin.Parse("enr1_" + secret + "_pin"); ok {
		t.Fatal("a node token parsed as a master join token")
	}
	if _, _, ok := masterjoin.Parse("mst1_" + secret); ok {
		t.Fatal("an incomplete token parsed")
	}
}

func TestExchangeRefusesADifferentCA(t *testing.T) {
	ca, _ := fleetca.New("fleet")
	other, _ := fleetca.New("other")
	h := modtest.NewHost("fi1")
	ctx := context.Background()
	now := time.Now()
	secret, _ := masterjoin.NewSecret()
	if err := masterjoin.Mint(ctx, hostWriter{h}, secret, "ru2", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	// the token names `other`'s fingerprint but the server signs with `ca`
	token := masterjoin.Token(secret, other)
	srv := httptest.NewServer(masterjoin.Handler(ca, hostWriter{h}, h, time.Now))
	defer srv.Close()
	if _, err := masterjoin.Exchange(ctx, srv.URL, token, "ru2", nil, nil); !errors.Is(err, masterjoin.ErrBadCA) {
		t.Fatalf("err = %v", err)
	}
}
