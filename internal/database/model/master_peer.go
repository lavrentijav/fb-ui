package model

// MasterPeer is another panel that serves the same subscriptions as this one.
// Peers are advertised to clients as fallback endpoints, so a client whose
// primary subscription host is blocked can retry elsewhere without a new link.
type MasterPeer struct {
	Id       int      `json:"id" form:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name     string   `json:"name" form:"name" gorm:"uniqueIndex" validate:"required" example:"eu-sub-2"`
	Remark   string   `json:"remark" form:"remark"`
	Scheme   string   `json:"scheme" form:"scheme" gorm:"default:https" validate:"omitempty,oneof=http https" example:"https"`
	Domain   string   `json:"domain" form:"domain" validate:"required" example:"sub2.example.com"`
	Port     int      `json:"port" form:"port" gorm:"default:2096" validate:"gte=1,lte=65535" example:"2096"`
	SubPath  string   `json:"subPath" form:"subPath" gorm:"column:sub_path;default:/sub/" example:"/sub/"`
	BasePath string   `json:"basePath" form:"basePath" gorm:"column:base_path;default:/" example:"/"`
	Ips      []string `json:"ips" form:"ips" gorm:"serializer:json"`
	Enable   bool     `json:"enable" form:"enable" gorm:"default:true" example:"true"`

	// AllowPrivateAddress opens the SSRF guard for lab setups where peers sit on
	// a private network; off by default, exactly like Node.
	AllowPrivateAddress bool `json:"allowPrivateAddress" form:"allowPrivateAddress" gorm:"column:allow_private_address;default:false"`

	// IsSelf marks the row describing this very panel. Set automatically when a
	// probe returns our own subscription signing key, and never advertised as a
	// fallback of itself. Admins can also set it by hand for an unsigned peer.
	IsSelf bool `json:"isSelf" form:"isSelf" gorm:"column:is_self;default:false"`

	// PublicKey is the peer's ed25519 subscription-signing key as learned from
	// its probe. Observed state — never user-edited.
	PublicKey string `json:"publicKey" gorm:"column:public_key"`

	Status        string `json:"status" gorm:"default:unknown" example:"online"` // online|offline|unknown
	LastHeartbeat int64  `json:"lastHeartbeat" example:"1700000000"`             // unix seconds, 0 = never
	LatencyMs     int    `json:"latencyMs" example:"42"`
	LastError     string `json:"lastError"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli" example:"1700000000"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli" example:"1700000000"`
}

func (MasterPeer) TableName() string { return "master_peers" }
