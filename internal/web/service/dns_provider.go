package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
// keeps the stored one, so the UI can edit a zone without re-pasting a token.
type DnsProviderRequest struct {
	Name       string `json:"name" form:"name" example:"corp"`
	Type       string `json:"type" form:"type" example:"cloudflare"`
	Zone       string `json:"zone" form:"zone" example:"corp.example"`
	ApiUrl     string `json:"apiUrl" form:"apiUrl" example:"https://ns1.example.com:53443"`
	Credential string `json:"credential" form:"credential" example:"cf-api-token"`
}

// dnsProviderFactory is swapped by tests to point providers at httptest.
var dnsProviderFactory = dnsprovider.New

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
	if cred := strings.TrimSpace(req.Credential); cred != "" {
		dst.Credential = cred
	}
	if dst.Name == "" {
		dst.Name = dst.Zone
	}
	switch dst.Type {
	case model.DnsProviderCloudflare, model.DnsProviderTechnitium:
	default:
		return common.NewErrorf("unknown DNS provider type %q", dst.Type)
	}
	if dst.Zone == "" || !strings.Contains(dst.Zone, ".") {
		return common.NewError("a DNS zone such as example.com is required")
	}
	if dst.Type == model.DnsProviderTechnitium && dst.ApiUrl == "" {
		return common.NewError("Technitium needs its API URL, e.g. https://ns1.example.com:53443")
	}
	if dst.ApiUrl != "" {
		u, err := url.Parse(dst.ApiUrl)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return common.NewErrorf("API URL %q must be an http(s) URL", dst.ApiUrl)
		}
	}
	if dst.Credential == "" {
		return common.NewError("an API token is required")
	}
	return nil
}

func (s *DnsProviderService) Create(req *DnsProviderRequest) (*model.DnsProvider, error) {
	row := &model.DnsProvider{}
	if err := s.fromRequest(row, req); err != nil {
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
	return database.GetDB().Save(row).Error
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
// add dialog can verify a token before it is stored.
func (s *DnsProviderService) Test(id int, req *DnsProviderRequest) error {
	row := &model.DnsProvider{}
	if id > 0 {
		var err error
		if row, err = s.load(id); err != nil {
			return err
		}
	}
	if req != nil {
		if err := s.fromRequest(row, req); err != nil {
			return err
		}
	}
	p, err := s.client(row)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.Check(ctx)
}

func (s *DnsProviderService) client(row *model.DnsProvider) (dnsprovider.Provider, error) {
	return dnsProviderFactory(dnsprovider.Config{
		Type:       row.Type,
		Zone:       row.Zone,
		Credential: row.Credential,
		BaseURL:    row.ApiUrl,
	})
}

// ClientFor returns the provider client and zone of a stored row.
func (s *DnsProviderService) ClientFor(id int) (dnsprovider.Provider, string, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, "", fmt.Errorf("DNS provider %d: %w", id, err)
	}
	p, err := s.client(row)
	return p, row.Zone, err
}

var errNoZoneForName = errors.New("no DNS provider manages this name")

// ForName picks the provider whose zone is the longest suffix of name, so a
// delegated sub-zone wins over its parent.
func (s *DnsProviderService) ForName(name string) (*model.DnsProvider, error) {
	var rows []*model.DnsProvider
	if err := database.GetDB().Find(&rows).Error; err != nil {
		return nil, err
	}
	var best *model.DnsProvider
	for _, r := range rows {
		if dnsprovider.InZone(name, r.Zone) && (best == nil || len(r.Zone) > len(best.Zone)) {
			best = r
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: %s", errNoZoneForName, name)
	}
	return best, nil
}
