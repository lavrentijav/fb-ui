package model

// Edge route kinds and actions; see internal/edge for what each one does.
const (
	EdgeKindSNI  = "sni"
	EdgeKindHTTP = "http"

	EdgeActionPanel  = "panel"
	EdgeActionTCP    = "tcp"
	EdgeActionReject = "reject"
	EdgeActionProxy  = "proxy"
)

// EdgeRoute is one rule of the shared public port. An sni route matches the
// TLS server name without decrypting and sends the connection to the panel or
// to a local address; an http route matches host and path inside the panel's
// own TLS and reverse-proxies to a local URL.
type EdgeRoute struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name      string `json:"name" form:"name" example:"reality"`
	Kind      string `json:"kind" form:"kind" gorm:"default:sni" example:"sni"`
	Enable    bool   `json:"enable" form:"enable" example:"true"`
	SortOrder int    `json:"sortOrder" form:"sortOrder" gorm:"column:sort_order;default:0"`

	// Match is the SNI (sni) or Host (http) pattern: exact, "*.zone", or ""
	// for no SNI / any host.
	Match      string `json:"match" form:"match" example:"www.microsoft.com"`
	PathPrefix string `json:"pathPrefix" form:"pathPrefix" gorm:"column:path_prefix" example:"/ws"`

	Action        string `json:"action" form:"action" example:"tcp"`
	Target        string `json:"target" form:"target" example:"127.0.0.1:8443"`
	ProxyProtocol int    `json:"proxyProtocol" form:"proxyProtocol" gorm:"column:proxy_protocol;default:0" example:"1"`
	StripPrefix   bool   `json:"stripPrefix" form:"stripPrefix" gorm:"column:strip_prefix"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (EdgeRoute) TableName() string { return "edge_routes" }
