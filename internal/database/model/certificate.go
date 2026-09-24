package model

// DNS provider types; they match dnsprovider.Type* so the service can hand the
// row straight to the provider factory.
const (
	DnsProviderCloudflare = "cloudflare"
	DnsProviderTechnitium = "technitium"
)

// DnsProvider is one DNS zone this panel may write records into: node A
// records during provisioning, ACME DNS-01 tokens and multi-address domains.
type DnsProvider struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name   string `json:"name" form:"name" gorm:"uniqueIndex" validate:"required" example:"corp"`
	Type   string `json:"type" form:"type" gorm:"default:cloudflare" validate:"required,oneof=cloudflare technitium" example:"cloudflare"`
	Zone   string `json:"zone" form:"zone" gorm:"index" validate:"required" example:"corp.example"`
	ApiUrl string `json:"apiUrl" form:"apiUrl" gorm:"column:api_url" example:"https://ns1.example.com:53443"`

	// Credential is write-only over the API, like a node's ApiToken.
	Credential    string `json:"-" form:"-" gorm:"column:credential"`
	HasCredential bool   `json:"hasCredential" gorm:"-" example:"true"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (DnsProvider) TableName() string { return "dns_providers" }

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
	Domains   []string `json:"domains" form:"domains" gorm:"serializer:json" validate:"required,min=1" example:"[\"msk1.corp.example\"]"`
	Challenge string   `json:"challenge" form:"challenge" gorm:"default:dns01" validate:"omitempty,oneof=dns01 http01 imported" example:"dns01"`

	// DnsProviderId must own every domain when Challenge is dns01.
	DnsProviderId int  `json:"dnsProviderId" form:"dnsProviderId" gorm:"column:dns_provider_id;index" example:"1"`
	AutoRenew     bool `json:"autoRenew" form:"autoRenew" gorm:"column:auto_renew" example:"true"`

	CertFile string `json:"certFile" gorm:"column:cert_file" example:"/root/cert/msk1.corp.example/fullchain.pem"`
	KeyFile  string `json:"keyFile" gorm:"column:key_file" example:"/root/cert/msk1.corp.example/privkey.pem"`

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
