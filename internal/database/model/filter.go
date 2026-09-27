package model

// Filter list kinds. They map straight onto the two matcher arrays an Xray
// routing rule accepts, which is why a list is one kind or the other rather
// than a mixed bag: the entries have to land in the right field.
const (
	FilterKindDomain = "domain"
	FilterKindIP     = "ip"
)

// Filter rule actions.
const (
	FilterActionBlock   = "block"
	FilterActionDirect  = "direct"
	FilterActionCascade = "cascade"
)

// FilterList is a named set of matchers an operator maintains once and reuses
// across rules — ad domains, a country's IP ranges, a bypass list. Entries are
// stored verbatim so Xray's own prefixes keep working: a domain list takes
// plain domains, "geosite:category-ads", "regexp:", "full:"; an IP list takes
// addresses, CIDRs and "geoip:ru".
type FilterList struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name   string `json:"name" form:"name" gorm:"uniqueIndex" validate:"required" example:"ads"`
	Remark string `json:"remark" form:"remark"`
	Kind   string `json:"kind" form:"kind" gorm:"default:domain;index" validate:"omitempty,oneof=domain ip" example:"domain"`

	Entries []string `json:"entries" form:"entries" gorm:"serializer:json"`
	Enable  bool     `json:"enable" form:"enable" gorm:"default:true" example:"true"`

	// SourceUrl makes the list follow a published one (antifilter, v2fly,
	// adblock, hosts files); what it yields is kept apart from Entries, which
	// stay the operator's own additions.
	SourceUrl     string   `json:"sourceUrl" form:"sourceUrl" gorm:"column:source_url" example:"https://antifilter.download/list/allyouneed.lst"`
	RefreshHours  int      `json:"refreshHours" form:"refreshHours" gorm:"column:refresh_hours;default:24" example:"24"`
	RemoteEntries []string `json:"-" gorm:"column:remote_entries;serializer:json"`
	RemoteCount   int      `json:"remoteCount" gorm:"column:remote_count" example:"8412"`
	FetchedAt     int64    `json:"fetchedAt" gorm:"column:fetched_at" example:"1700000000"`
	FetchError    string   `json:"fetchError" gorm:"column:fetch_error"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (FilterList) TableName() string { return "filter_lists" }

// Matchers is everything the list contributes to a rule: the operator's own
// entries first, then whatever the source URL yielded.
func (l *FilterList) Matchers() []string {
	if len(l.RemoteEntries) == 0 {
		return l.Entries
	}
	return append(append(make([]string, 0, len(l.Entries)+len(l.RemoteEntries)), l.Entries...), l.RemoteEntries...)
}

// FilterRule is a filtering node on the topology canvas: traffic entering the
// named inbounds of one panel is matched against the referenced lists and sent
// to the chosen action. It is the stored form of one Xray routing rule.
type FilterRule struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name   string `json:"name" form:"name" validate:"required" example:"block ads"`
	Remark string `json:"remark" form:"remark"`

	// PanelId is the panel that enforces the rule, with 0 meaning this one —
	// the same convention Inbound.NodeID uses for ownership.
	PanelId int `json:"panelId" form:"panelId" gorm:"column:panel_id;index" example:"0"`

	// SourceInboundTags narrows the rule to specific inbounds on that panel.
	// Empty means every inbound it owns, which is what a panel-wide block wants.
	SourceInboundTags []string `json:"sourceInboundTags" form:"sourceInboundTags" gorm:"serializer:json;column:source_inbound_tags"`

	ListIds []int  `json:"listIds" form:"listIds" gorm:"serializer:json;column:list_ids"`
	Action  string `json:"action" form:"action" gorm:"default:block" validate:"omitempty,oneof=block direct cascade" example:"block"`

	// CascadeLinkId names the edge matching traffic is diverted onto when the
	// action is "cascade"; ignored for the other actions.
	CascadeLinkId int `json:"cascadeLinkId" form:"cascadeLinkId" gorm:"column:cascade_link_id"`

	// SortOrder decides which rule wins when several match: Xray takes the
	// first hit, so the order here is the order emitted into the config.
	SortOrder int  `json:"sortOrder" form:"sortOrder" gorm:"column:sort_order;default:0"`
	Enable    bool `json:"enable" form:"enable" gorm:"default:true" example:"true"`

	// Applied records the last time the rule reached the panel's Xray config;
	// 0 means it is still pending.
	Applied   int64 `json:"applied" gorm:"default:0"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (FilterRule) TableName() string { return "filter_rules" }
