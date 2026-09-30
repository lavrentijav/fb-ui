package certs

import (
	"context"
	"crypto"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/certissuer"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

// AccountTable holds the ACME account every master orders with, so the
// CA's rate limits and ToS acceptance follow the fleet, not one master.
const AccountTable = "acme"

// ACME issues through an ACME CA with dns01, so wildcards work and no
// master needs port 80.
type ACME struct {
	Directory string // default Let's Encrypt
	Email     string
	// DNSFor picks the DNS account and zone that hold name.
	DNSFor     func(name string) (dnsprovider.Provider, string, error)
	HTTPClient *http.Client
	// Propagated replaces the authoritative-nameserver check (tests).
	Propagated func(ctx context.Context, zone, name string, values []string) error
	Logf       func(format string, args ...any)
}

// Issuer orders with the fleet's account key, created by the first issue;
// that runs under a lease, and a rare race only leaves one account unused.
func (a ACME) Issuer(m *master.Master, state Reader) Issuer {
	return func(ctx context.Context, names []string) (Issued, error) {
		key, err := a.account(ctx, m, state)
		if err != nil {
			return Issued{}, err
		}
		dir := a.Directory
		if dir == "" {
			dir = certissuer.LetsEncryptDirectory
		}
		res, err := certissuer.Issue(ctx, certissuer.Request{
			DirectoryURL: dir, AccountKey: key, Email: a.Email,
			Domains: append([]string(nil), names...), Challenge: certissuer.DNS01,
			DNSFor: a.DNSFor, Propagated: a.Propagated, HTTPClient: a.HTTPClient, Log: a.Logf,
		})
		if err != nil {
			return Issued{}, err
		}
		return Issued{CertPEM: string(res.CertPEM), KeyPEM: string(res.KeyPEM), NotAfter: res.NotAfter}, nil
	}
}

func (a ACME) account(ctx context.Context, m *master.Master, state Reader) (crypto.Signer, error) {
	if rec, ok := state.Get(AccountTable, "account"); ok {
		if keyPEM, ok := field[string](rec, "keyPem"); ok && keyPEM != "" {
			return certissuer.ParseAccountKey([]byte(keyPEM))
		}
	}
	key, keyPEM, err := certissuer.NewAccountKey()
	if err != nil {
		return nil, err
	}
	if _, err := m.Set(ctx, AccountTable, "account", map[string]any{"keyPem": string(keyPEM)}); err != nil {
		return nil, err
	}
	return key, nil
}
