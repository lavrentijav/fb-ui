package model

import "strconv"

// CascadeLink is one edge of the network graph: traffic arriving on the source
// panel's inbound is forwarded to an inbound on the target panel instead of
// leaving there. It is the stored form of what an operator otherwise assembles
// by hand — an outbound plus a routing rule on the source panel.
//
// Panels are referenced by node id, with 0 meaning this panel itself, matching
// how Inbound.NodeID marks ownership (nil = local).
type CascadeLink struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Remark string `json:"remark" form:"remark"`

	SourcePanelId    int    `json:"sourcePanelId" form:"sourcePanelId" gorm:"column:source_panel_id;index" example:"2"`
	SourceInboundTag string `json:"sourceInboundTag" form:"sourceInboundTag" gorm:"column:source_inbound_tag" validate:"required" example:"in-39101-tcp"`

	TargetPanelId   int `json:"targetPanelId" form:"targetPanelId" gorm:"column:target_panel_id;index" example:"0"`
	TargetInboundId int `json:"targetInboundId" form:"targetInboundId" gorm:"column:target_inbound_id" validate:"required" example:"7"`

	// TargetClientEmail picks which credential the generated outbound dials
	// with. Empty means the first enabled client on the target inbound.
	TargetClientEmail string `json:"targetClientEmail" form:"targetClientEmail" gorm:"column:target_client_email"`

	Enable bool `json:"enable" form:"enable" gorm:"default:true" example:"true"`

	// Applied records the last time the link was materialized into the source
	// panel's Xray config; 0 means it is still pending.
	Applied   int64 `json:"applied" gorm:"default:0"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (CascadeLink) TableName() string { return "cascade_links" }

// OutboundTag is the tag the generated outbound carries on the source panel.
// Deterministic so re-applying a link replaces its own outbound instead of
// piling up duplicates.
func (l CascadeLink) OutboundTag() string {
	return "cascade-" + strconv.Itoa(l.Id)
}
