package model

// InboundTemplate is a reusable set of inbounds, such as "RU entry: one
// WS+TLS inbound per exit country". Applying it to a node creates the missing
// inbounds there and attaches the clients their source inbounds had.
type InboundTemplate struct {
	Id     int                   `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name   string                `json:"name" form:"name" gorm:"uniqueIndex" example:"RU entry"`
	Remark string                `json:"remark" form:"remark" example:"4 cascade countries + 443 twins"`
	Items  []InboundTemplateItem `json:"items" gorm:"serializer:json"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (InboundTemplate) TableName() string { return "inbound_templates" }

// InboundTemplateItem is one inbound without its clients. {{NODE_DOMAIN}} and
// {{NODE_NAME}} in its text fields become the target node's address and name.
type InboundTemplateItem struct {
	Remark            string `json:"remark" example:"🇩🇪 Germany"`
	Enable            bool   `json:"enable" example:"true"`
	Protocol          string `json:"protocol" example:"vless"`
	Listen            string `json:"listen" example:""`
	Port              int    `json:"port" example:"2053"`
	Settings          string `json:"settings"`
	StreamSettings    string `json:"streamSettings"`
	Sniffing          string `json:"sniffing"`
	SubSortIndex      int    `json:"subSortIndex" example:"1"`
	ShareAddrStrategy string `json:"shareAddrStrategy" example:"custom"`
	ShareAddr         string `json:"shareAddr" example:"ru.walk.example.com"`
	// ClientsFromInboundId: at apply time the new inbound gets the clients
	// this inbound has then, so later sign-ups are not frozen into the template.
	ClientsFromInboundId int `json:"clientsFromInboundId" example:"116"`
}
