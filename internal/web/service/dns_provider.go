package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// DnsProviderService stores DNS zones the panel may write into and builds
// dnsprovider clients for them. Credentials never leave through its views.
type DnsProviderService struct{}

// DnsProviderRequest is the add/update payload. An empty Credential on update
// keeps the stored one, so the UI can edit an account without re-pasting a
// token. An empty Zone manages every zone the token can see.
type DnsProviderRequest struct {
	Name       string `json:"name" form:"name" example:"technitium"`
	Type       string `json:"type" form:"type" example:"technitium"`
	Zone       string `json:"zone" form:"zone" example:""`
	ApiUrl     string `json:"apiUrl" form:"apiUrl" example:"ns1.example.com"`
	ApiPort    int    `json:"apiPort" form:"apiPort" example:"5380"`
	Credential string `json:"credential" form:"credential" example:"api-token"`
	// Subdomains replaces the stored list when present; omitted keeps it.
	Subdomains *[]string `json:"subdomains,omitempty" form:"-" example:"[\"walk.corp.example\"]"`
}

// DnsZone is one zone the panel can write, with the account that owns it.
type DnsZone struct {
	Zone         string `json:"zone" example:"corp.example"`
	Subdomain    bool   `json:"subdomain" example:"false"`
	ProviderId   int    `json:"providerId" example:"1"`
	ProviderName string `json:"providerName" example:"technitium"`
	ProviderType string `json:"providerType" example:"technitium"`
}

// dnsProviderFactory and dnsZoneLister are swapped by tests.
var (
	dnsProviderFactory = dnsprovider.New
	dnsZoneLister      = dnsprovider.ListZones
)

// dnsResyncEvery bounds how often a name no zone covers triggers a rediscovery,
// so a zone just added in Technitium is picked up without a click.
const dnsResyncEvery = time.Minute

func (s *DnsProviderService) List() ([]*model.DnsProvider, error) {
	var rows []*model.DnsProvider
	if err := database.GetDB().Order("zone asc, id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		redactDnsProvider(r)
	}
	return rows, nil
}

func (s *DnsProviderService) GetById(id int) (*model.DnsProvider, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, err
	}
	redactDnsProvider(row)
	return row, nil
}

func (s *DnsProviderService) load(id int) (*model.DnsProvider, error) {
	row := &model.DnsProvider{}
	if err := database.GetDB().Where("id = ?", id).First(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func redactDnsProvider(r *model.DnsProvider) {
	r.HasCredential = r.Credential != ""
	r.Credential = ""
}

func (s *DnsProviderService) fromRequest(dst *model.DnsProvider, req *DnsProviderRequest) error {
	dst.Name = strings.TrimSpace(req.Name)
	dst.Type = strings.TrimSpace(req.Type)
	dst.Zone = dnsprovider.NormalizeName(req.Zone)
	dst.ApiUrl = strings.TrimRight(strings.TrimSpace(req.ApiUrl), "/")
	dst.ApiPort = req.ApiPort
	if cred := strings.Trim(strings.TrimSpace(req.Credential), "\"'`"); cred != "" {
		dst.Credential = cred
	}
	switch dst.Type {
	case model.DnsProviderCloudflare, model.DnsProviderTechnitium:
	default:
		return common.NewErrorf("unknown DNS provider type %q", dst.Type)
	}
	if dst.Name == "" {
		dst.Name = dst.Zone
	}
	if dst.Name == "" {
		dst.Name = dst.Type
		if host := apiHost(dst.ApiUrl); host != "" {
			dst.Name = host
		}
	}
	if dst.Zone != "" && !strings.Contains(dst.Zone, ".") {
		return common.NewError("a DNS zone looks like example.com; leave it empty to use every zone the token sees")
	}
	if dst.ApiPort < 0 || dst.ApiPort > 65535 {
		return common.NewError("API port must be 1-65535")
	}
	if dst.Type == model.DnsProviderTechnitium && dst.ApiUrl == "" {
		return common.NewError("Technitium needs its API URL, e.g. https://ns1.example.com:53443")
	}
	if dst.ApiUrl != "" {
		if _, err := dnsApiBaseURL(dst.ApiUrl, dst.ApiPort); err != nil {
			return err
		}
	}
	if dst.Credential == "" {
		return common.NewError("an API token is required")
	}
	if req.Subdomains != nil {
		dst.Subdomains = nil
		for _, sub := range *req.Subdomains {
			if sub = dnsprovider.NormalizeName(sub); sub != "" && !slices.Contains(dst.Subdomains, sub) {
				dst.Subdomains = append(dst.Subdomains, sub)
			}
		}
	}
	return nil
}

// checkSubdomains runs once the zones are known: a subdomain must sit strictly
// below one of them, or records for it would land in no zone this token writes.
func checkSubdomains(row *model.DnsProvider) error {
	for _, sub := range row.Subdomains {
		zone := dnsprovider.ZoneFor(sub, row.ManagedZones())
		if zone == "" || zone == sub {
			return common.NewErrorf("%s is not below any zone of %s (%s)", sub, row.Name, strings.Join(row.ManagedZones(), ", "))
		}
	}
	return nil
}

// dnsApiBaseURL joins what the operator typed: a bare host speaks http on
// 5380 by default and https only on 443/53443; the port field always wins.
func dnsApiBaseURL(raw string, port int) (string, error) {
	if !strings.Contains(raw, "://") {
		scheme := "http"
		if port == 443 || port == 53443 {
			scheme = "https"
		}
		raw = scheme + "://" + raw
		if port == 0 {
			port = 5380
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", common.NewErrorf("API URL %q must be a host or an http(s) URL", raw)
	}
	if port > 0 {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(port))
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func apiHost(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// discover fills Zones from the provider; a pinned Zone is only checked.
func (s *DnsProviderService) discover(ctx context.Context, row *model.DnsProvider) error {
	if row.Zone != "" {
		p, err := s.client(row, row.Zone)
		if err != nil {
			return err
		}
		if err := p.Check(ctx); err != nil {
			return err
		}
		row.Zones = []string{row.Zone}
	} else {
		base, err := s.baseURL(row)
		if err != nil {
			return err
		}
		zones, err := dnsZoneLister(ctx, dnsprovider.Config{Type: row.Type, Credential: row.Credential, BaseURL: base})
		if err != nil {
			return err
		}
		if len(zones) == 0 {
			return common.NewError("the token sees no writable (primary) zone")
		}
		row.Zones = zones
	}
	row.ZonesSyncedAt = time.Now().Unix()
	return nil
}

func (s *DnsProviderService) Create(req *DnsProviderRequest) (*model.DnsProvider, error) {
	row := &model.DnsProvider{}
	if err := s.fromRequest(row, req); err != nil {
		return nil, err
	}
	if err := s.discoverNow(row); err != nil {
		return nil, err
	}
	if err := checkSubdomains(row); err != nil {
		return nil, err
	}
	if err := database.GetDB().Create(row).Error; err != nil {
		return nil, err
	}
	redactDnsProvider(row)
	return row, nil
}

func (s *DnsProviderService) Update(id int, req *DnsProviderRequest) error {
	row, err := s.load(id)
	if err != nil {
		return err
	}
	if err := s.fromRequest(row, req); err != nil {
		return err
	}
	if err := s.discoverNow(row); err != nil {
		return err
	}
	if err := checkSubdomains(row); err != nil {
		return err
	}
	return database.GetDB().Save(row).Error
}

func (s *DnsProviderService) discoverNow(row *model.DnsProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.discover(ctx, row)
}

// Sync rediscovers the zones of one account, or of all when id is 0.
func (s *DnsProviderService) Sync(id int) error {
	var rows []*model.DnsProvider
	q := database.GetDB()
	if id > 0 {
		q = q.Where("id = ?", id)
	}
	if err := q.Find(&rows).Error; err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		if err := s.discoverNow(row); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", row.Name, err))
			continue
		}
		// A struct update, not a map: only it runs the json serializer on Zones.
		if err := database.GetDB().Model(row).Select("zones", "zones_synced_at").Updates(row).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Zones lists every zone the panel can write, for zone pickers.
func (s *DnsProviderService) Zones() ([]DnsZone, error) {
	var rows []*model.DnsProvider
	if err := database.GetDB().Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	var out []DnsZone
	for _, r := range rows {
		for _, z := range r.BaseDomains() {
			out = append(out, DnsZone{Zone: z, Subdomain: !slices.Contains(r.ManagedZones(), z), ProviderId: r.Id, ProviderName: r.Name, ProviderType: r.Type})
		}
	}
	slices.SortStableFunc(out, func(a, b DnsZone) int { return strings.Compare(a.Zone, b.Zone) })
	return out, nil
}

func (s *DnsProviderService) Delete(id int) error {
	var inUse int64
	if err := database.GetDB().Model(&model.Certificate{}).Where("dns_provider_id = ?", id).Count(&inUse).Error; err != nil {
		return err
	}
	if inUse > 0 {
		return common.NewErrorf("%d certificate(s) renew through this DNS provider; move or delete them first", inUse)
	}
	return database.GetDB().Where("id = ?", id).Delete(&model.DnsProvider{}).Error
}

// Test checks a stored provider, or an unsaved request when id is 0, so the
// add dialog can verify a token before it is stored. It returns the zones
// the token would manage.
func (s *DnsProviderService) Test(id int, req *DnsProviderRequest) ([]string, error) {
	row := &model.DnsProvider{}
	if id > 0 {
		var err error
		if row, err = s.load(id); err != nil {
			return nil, err
		}
	}
	if req != nil {
		if err := s.fromRequest(row, req); err != nil {
			return nil, err
		}
	}
	if err := s.discoverNow(row); err != nil {
		return nil, err
	}
	return row.Zones, nil
}

func (s *DnsProviderService) baseURL(row *model.DnsProvider) (string, error) {
	if row.ApiUrl == "" {
		return "", nil
	}
	return dnsApiBaseURL(row.ApiUrl, row.ApiPort)
}

func (s *DnsProviderService) client(row *model.DnsProvider, zone string) (dnsprovider.Provider, error) {
	base, err := s.baseURL(row)
	if err != nil {
		return nil, err
	}
	return dnsProviderFactory(dnsprovider.Config{
		Type:       row.Type,
		Zone:       zone,
		Credential: row.Credential,
		BaseURL:    base,
	})
}

// ClientFor returns the client and zone of account id that will hold name.
func (s *DnsProviderService) ClientFor(id int, name string) (dnsprovider.Provider, string, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, "", fmt.Errorf("DNS provider %d: %w", id, err)
	}
	zone := dnsprovider.ZoneFor(name, row.ManagedZones())
	if zone == "" && row.Zone == "" {
		if err := s.Sync(id); err == nil {
			if row, err = s.load(id); err != nil {
				return nil, "", err
			}
			zone = dnsprovider.ZoneFor(name, row.ManagedZones())
		}
	}
	if zone == "" {
		return nil, "", fmt.Errorf("%w: %s is not in any zone of %s", errNoZoneForName, name, row.Name)
	}
	p, err := s.client(row, zone)
	return p, zone, err
}

var (
	errNoZoneForName = errors.New("no DNS provider manages this name")
	lastDnsResync    atomic.Int64
)

// ForName picks the account and zone that hold name: the longest zone across
// all accounts wins, so a delegated sub-zone beats its parent. A miss
// rediscovers zones once a minute before giving up.
func (s *DnsProviderService) ForName(name string) (*model.DnsProvider, string, error) {
	row, zone, err := s.bestZone(name)
	if err == nil || row != nil {
		return row, zone, err
	}
	if now := time.Now().Unix(); now-lastDnsResync.Load() >= int64(dnsResyncEvery/time.Second) {
		lastDnsResync.Store(now)
		_ = s.Sync(0)
		return s.bestZone(name)
	}
	return nil, "", fmt.Errorf("%w: %s", errNoZoneForName, name)
}

func (s *DnsProviderService) bestZone(name string) (*model.DnsProvider, string, error) {
	var rows []*model.DnsProvider
	if err := database.GetDB().Order("id asc").Find(&rows).Error; err != nil {
		return nil, "", err
	}
	var best *model.DnsProvider
	bestZone := ""
	for _, r := range rows {
		if z := dnsprovider.ZoneFor(name, r.ManagedZones()); len(z) > len(bestZone) {
			best, bestZone = r, z
		}
	}
	if best == nil {
		return nil, "", fmt.Errorf("%w: %s", errNoZoneForName, name)
	}
	return best, bestZone, nil
}
