// Package certissuer obtains certificates from an ACME CA (Let's Encrypt by
// default). It is a leaf package: DNS writes go through dnsprovider and the
// caller decides where the resulting PEM files are stored.
package certissuer

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
)

const LetsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"

// Challenge kinds.
const (
	DNS01  = "dns01"
	HTTP01 = "http01"
)

// Request describes one issuance. AccountKey is reused across issuances so the
// CA's per-account rate limits and ToS acceptance carry over.
type Request struct {
	DirectoryURL string
	AccountKey   crypto.Signer
	Email        string
	Domains      []string
	Challenge    string

	// DNS and Zone serve dns01; HTTPAddr (default ":80") serves http01.
	DNS      dnsprovider.Provider
	Zone     string
	HTTPAddr string
	// DNSFor, when set, picks the account and zone per name, so one
	// certificate can span zones held by different DNS accounts.
	DNSFor func(name string) (dnsprovider.Provider, string, error)

	// Propagated blocks until every TXT value is visible; nil means the
	// authoritative-nameserver check. Tests swap it out.
	Propagated func(ctx context.Context, zone, name string, values []string) error
	Log        func(format string, args ...any)
	HTTPClient *http.Client
}

// Result is the issued certificate.
type Result struct {
	CertPEM    []byte
	KeyPEM     []byte
	NotBefore  time.Time
	NotAfter   time.Time
	Issuer     string
	AccountURI string
}

// NewAccountKey returns a fresh ECDSA P-256 account key as PEM.
func NewAccountKey() (crypto.Signer, []byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParseAccountKey decodes a PEM key produced by NewAccountKey.
func ParseAccountKey(pemBytes []byte) (crypto.Signer, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		return nil, errors.New("certissuer: account key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("certissuer: account key: %w", err)
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("certissuer: account key cannot sign")
	}
	return s, nil
}

func (r *Request) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Request) validate() error {
	if r.AccountKey == nil {
		return errors.New("certissuer: account key is required")
	}
	if len(r.Domains) == 0 {
		return errors.New("certissuer: at least one domain is required")
	}
	for i, d := range r.Domains {
		d = dnsprovider.NormalizeName(d)
		if d == "" || strings.ContainsAny(d, " /:") {
			return fmt.Errorf("certissuer: invalid domain %q", r.Domains[i])
		}
		r.Domains[i] = d
	}
	switch r.Challenge {
	case DNS01:
		if r.DNSFor == nil && (r.DNS == nil || r.Zone == "") {
			return errors.New("certissuer: dns01 needs a DNS provider")
		}
		for _, d := range r.Domains {
			if _, _, err := r.dnsFor(strings.TrimPrefix(d, "*.")); err != nil {
				return err
			}
		}
	case HTTP01:
		for _, d := range r.Domains {
			if strings.HasPrefix(d, "*.") {
				return fmt.Errorf("certissuer: wildcard %s needs dns01", d)
			}
		}
	default:
		return fmt.Errorf("certissuer: unknown challenge %q", r.Challenge)
	}
	return nil
}

// Issue runs one full ACME order. It always removes the challenge records or
// listener it created, even when validation fails.
func Issue(ctx context.Context, req Request) (*Result, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	dir := req.DirectoryURL
	if dir == "" {
		dir = LetsEncryptDirectory
	}
	client := &acme.Client{Key: req.AccountKey, DirectoryURL: dir}
	if req.HTTPClient != nil {
		client.HTTPClient = req.HTTPClient
	}

	acct := &acme.Account{}
	if req.Email != "" {
		acct.Contact = []string{"mailto:" + req.Email}
	}
	reg, err := client.Register(ctx, acct, acme.AcceptTOS)
	if errors.Is(err, acme.ErrAccountAlreadyExists) {
		reg, err = client.GetReg(ctx, "")
	}
	if err != nil {
		return nil, fmt.Errorf("acme account: %w", err)
	}
	req.logf("ACME account ready: %s", reg.URI)

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(req.Domains...))
	if err != nil {
		return nil, fmt.Errorf("acme order: %w", err)
	}
	pending, err := pendingChallenges(ctx, client, order, req.Challenge)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		cleanup, err := present(ctx, client, &req, pending)
		defer cleanup()
		if err != nil {
			return nil, err
		}
		for _, p := range pending {
			if _, err := client.Accept(ctx, p.chal); err != nil {
				return nil, fmt.Errorf("acme accept %s: %w", p.domain, err)
			}
		}
		for _, p := range pending {
			if _, err := client.WaitAuthorization(ctx, p.authzURL); err != nil {
				return nil, fmt.Errorf("acme validation of %s failed: %w", p.domain, err)
			}
			req.logf("validated %s", p.domain)
		}
	}
	orderURI := order.URI
	if order, err = client.WaitOrder(ctx, orderURI); err != nil {
		return nil, fmt.Errorf("acme order not ready: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: strings.TrimPrefix(req.Domains[0], "*.")},
		DNSNames: req.Domains,
	}, certKey)
	if err != nil {
		return nil, err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		// A CA that omits Location on finalize leaves x/crypto polling "", so
		// fall back to the order URL we already hold.
		if chain, err = fetchFinalized(ctx, client, orderURI, err); err != nil {
			return nil, fmt.Errorf("acme finalize: %w", err)
		}
	}
	return buildResult(chain, certKey, reg.URI)
}

func fetchFinalized(ctx context.Context, client *acme.Client, orderURI string, finalizeErr error) ([][]byte, error) {
	o, err := client.WaitOrder(ctx, orderURI)
	if err != nil || o.Status != acme.StatusValid || o.CertURL == "" {
		return nil, finalizeErr
	}
	return client.FetchCert(ctx, o.CertURL, true)
}

func (r *Request) dnsFor(name string) (dnsprovider.Provider, string, error) {
	if r.DNSFor != nil {
		return r.DNSFor(name)
	}
	if !dnsprovider.InZone(name, r.Zone) {
		return nil, "", fmt.Errorf("certissuer: %s is not in zone %s", name, r.Zone)
	}
	return r.DNS, r.Zone, nil
}

type pendingChallenge struct {
	domain   string
	authzURL string
	chal     *acme.Challenge
}

func pendingChallenges(ctx context.Context, client *acme.Client, order *acme.Order, kind string) ([]pendingChallenge, error) {
	want := "dns-01"
	if kind == HTTP01 {
		want = "http-01"
	}
	var out []pendingChallenge
	for _, u := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("acme authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		domain := authz.Identifier.Value
		if authz.Wildcard {
			domain = "*." + domain
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == want {
				chal = c
				break
			}
		}
		if chal == nil {
			return nil, fmt.Errorf("acme: CA offers no %s challenge for %s", want, domain)
		}
		out = append(out, pendingChallenge{domain: domain, authzURL: u, chal: chal})
	}
	return out, nil
}

func present(ctx context.Context, client *acme.Client, req *Request, pending []pendingChallenge) (func(), error) {
	if req.Challenge == HTTP01 {
		return presentHTTP01(ctx, client, req, pending)
	}
	return presentDNS01(ctx, client, req, pending)
}

func buildResult(chain [][]byte, key *ecdsa.PrivateKey, accountURI string) (*Result, error) {
	if len(chain) == 0 {
		return nil, errors.New("acme: CA returned an empty chain")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, fmt.Errorf("acme: parse issued certificate: %w", err)
	}
	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	issuer := leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		issuer = strings.TrimSpace(leaf.Issuer.Organization[0] + " " + issuer)
	}
	return &Result{
		CertPEM:    certPEM,
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
		Issuer:     issuer,
		AccountURI: accountURI,
	}, nil
}
