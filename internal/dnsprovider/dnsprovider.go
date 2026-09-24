// Package dnsprovider manages DNS records at a hosted-zone provider. It is a
// leaf package: callers hand it credentials and it never reads the database.
package dnsprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Provider types accepted by New.
const (
	TypeCloudflare = "cloudflare"
	TypeTechnitium = "technitium"
)

// Provider converges the records of one name. SetRecords is the only write
// verb on purpose: node A records, ACME TXT tokens and multi-address cascade
// domains are all "make this name resolve to exactly these values".
type Provider interface {
	SetRecords(ctx context.Context, name, rtype string, values []string, ttl int) error
	DeleteRecords(ctx context.Context, name, rtype string) error
	// Check verifies the credential can manage the zone without writing.
	Check(ctx context.Context) error
}

// Config is what an operator stores for one zone.
type Config struct {
	Type       string
	Zone       string
	Credential string
	// BaseURL is the API endpoint: required for a self-hosted Technitium server,
	// optional for Cloudflare (tests point it at httptest).
	BaseURL string
}

var ErrUnknownType = errors.New("dnsprovider: unknown provider type")

// New builds the provider for cfg.
func New(cfg Config) (Provider, error) {
	zone := NormalizeName(cfg.Zone)
	if zone == "" {
		return nil, errors.New("dnsprovider: zone is required")
	}
	if strings.TrimSpace(cfg.Credential) == "" {
		return nil, errors.New("dnsprovider: credential is required")
	}
	switch cfg.Type {
	case TypeCloudflare:
		return newCloudflare(zone, strings.TrimSpace(cfg.Credential), cfg.BaseURL), nil
	case TypeTechnitium:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return nil, errors.New("dnsprovider: technitium needs the server API URL")
		}
		return newTechnitium(zone, strings.TrimSpace(cfg.Credential), cfg.BaseURL), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, cfg.Type)
	}
}

// NormalizeName lower-cases a DNS name and drops the trailing root dot.
func NormalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// InZone reports whether name is the zone apex or a name below it.
func InZone(name, zone string) bool {
	name, zone = NormalizeName(name), NormalizeName(zone)
	return name == zone || strings.HasSuffix(name, "."+zone)
}
