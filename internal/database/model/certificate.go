package model

// DNS provider types; they match dnsprovider.Type* so the service can hand the
// row straight to the provider factory.
const (
	DnsProviderCloudflare = "cloudflare"
	DnsProviderTechnitium = "technitium"
)

// DnsProvider is one DNS account this panel may write records into: node A
// records during provisioning, ACME DNS-01 tokens and multi-address domains.
// An empty Zone means every zone the token can see, discovered into Zones.
type DnsProvider struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name    string `json:"name" form:"name" gorm:"uniqueIndex" validate:"required" example:"fire-core"`
	Type    string `json:"type" form:"type" gorm:"default:cloudflare" validate:"required,oneof=cloudflare technitium" example:"technitium"`
	Zone    string `json:"zone" form:"zone" gorm:"index" example:""`
	ApiUrl  string `json:"apiUrl" form:"apiUrl" gorm:"column:api_url" example:"ns1.example.com"`
	ApiPort int    `json:"apiPort" form:"apiPort" gorm:"column:api_port" example:"5380"`

	Zones         []string `json:"zones" gorm:"serializer:json" example:"[\"fire-core.ru\",\"boykisser.online\"]"`
	ZonesSyncedAt int64    `json:"zonesSyncedAt" gorm:"column:zones_synced_at" example:"1700000000"`

	// Credential is write-only over the API, like a node's ApiToken.
	Credential    string `json:"-" form:"-" gorm:"column:credential"`
	HasCredential bool   `json:"hasCredential" gorm:"-" example:"true"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (DnsProvider) TableName() string { return "dns_providers" }

// ManagedZones is Zone alone when pinned, else every discovered zone.
func (p *DnsProvider) ManagedZones() []string {
	if p.Zone != "" {
		return []string{p.Zone}
	}
	return p.Zones
}

// ACME challenge kinds a Certificate can be issued with. "imported" marks a
// certificate found on disk (acme.sh, manual upload) that we only watch.
const (
	CertChallengeDNS01    = "dns01"
	CertChallengeHTTP01   = "http01"
	CertChallengeImported = "imported"
)

// Certificate lifecycle states shown in the UI.
const (
	CertStatusPending = "pending"
	CertStatusIssuing = "issuing"
	CertStatusValid   = "valid"
	CertStatusError   = "error"
)

// Certificate is a TLS certificate the panel issues and renews, or only
// watches for expiry. Files live at CertFile/KeyFile so Xray and the panel
// read them exactly as they read a hand-installed certificate.
type Certificate struct {
	Id        int      `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Domains   []string `json:"domains" form:"domains" gorm:"serializer:json" validate:"required,min=1" example:"[\"msk1.fire-core.ru\"]"`
	Challenge string   `json:"challenge" form:"challenge" gorm:"default:dns01" validate:"omitempty,oneof=dns01 http01 imported" example:"dns01"`

	// DnsProviderId must own every domain when Challenge is dns01.
	DnsProviderId int  `json:"dnsProviderId" form:"dnsProviderId" gorm:"column:dns_provider_id;index" example:"1"`
	AutoRenew     bool `json:"autoRenew" form:"autoRenew" gorm:"column:auto_renew" example:"true"`

	// NodeId > 0: ordered here for that node and pushed to it after every
	// issue, so nodes need neither acme.sh nor the DNS token.
	NodeId    int    `json:"nodeId" form:"nodeId" gorm:"column:node_id;index" example:"0"`
	PushedAt  int64  `json:"pushedAt" gorm:"column:pushed_at" example:"1700000000"`
	PushError string `json:"pushError" gorm:"column:push_error"`

	CertFile string `json:"certFile" gorm:"column:cert_file" example:"/root/cert/msk1.fire-core.ru/fullchain.pem"`
	KeyFile  string `json:"keyFile" gorm:"column:key_file" example:"/root/cert/msk1.fire-core.ru/privkey.pem"`

	Issuer        string `json:"issuer" example:"Let's Encrypt R11"`
	NotBefore     int64  `json:"notBefore" gorm:"column:not_before" example:"1700000000"`
	NotAfter      int64  `json:"notAfter" gorm:"column:not_after;index" example:"1707776000"`
	Status        string `json:"status" gorm:"default:pending" example:"valid"`
	LastError     string `json:"lastError" gorm:"column:last_error"`
	LastAttemptAt int64  `json:"lastAttemptAt" gorm:"column:last_attempt_at" example:"1700000000"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (Certificate) TableName() string { return "certificates" }

// AcmeAccount is the registered ACME account key for one directory. One row
// per directory URL; the key never leaves the database.
type AcmeAccount struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Directory string `json:"directory" gorm:"uniqueIndex"`
	Email     string `json:"email"`
	KeyPem    string `json:"-" gorm:"column:key_pem"`
	Uri       string `json:"uri"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime:milli"`
}

func (AcmeAccount) TableName() string { return "acme_accounts" }
