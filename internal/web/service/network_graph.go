package service

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// SelfPanelId is the graph id of the panel serving the request. Inbounds owned
// by it carry a nil NodeID, so 0 is free to mean "here".
const SelfPanelId = 0

// GraphInbound is one inbound as the topology editor needs it: enough to draw a
// port on the panel and to dial it from another panel.
type GraphInbound struct {
	Id       int    `json:"id"`
	Tag      string `json:"tag"`
	Remark   string `json:"remark"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Enable   bool   `json:"enable"`
	Clients  int    `json:"clients"`
}

// GraphPanel is one vertex: a panel, with the inbounds it owns.
type GraphPanel struct {
	Id       int            `json:"id"`
	Name     string         `json:"name"`
	Role     string         `json:"role"`
	Status   string         `json:"status"`
	Address  string         `json:"address"`
	Self     bool           `json:"self"`
	Enable   bool           `json:"enable"`
	Inbounds []GraphInbound `json:"inbounds"`
}

// NetworkGraph is the whole topology in one response: what exists and how it is
// wired. The editor renders it directly. Filter rules are vertices too — they
// sit between an inbound and where its traffic ends up — and the lists they
// reference travel along so the canvas can label them without a second call.
type NetworkGraph struct {
	Panels  []GraphPanel         `json:"panels"`
	Links   []*model.CascadeLink `json:"links"`
	Filters []*model.FilterRule  `json:"filters"`
	Lists   []*model.FilterList  `json:"lists"`
}

type NetworkService struct{}

// Graph projects the registry and the inbound table into the topology view.
// Nothing here is a second source of truth: panels are node rows, inbounds are
// grouped by the NodeID column that already records ownership.
func (s *NetworkService) Graph() (*NetworkGraph, error) {
	db := database.GetDB()

	var nodes []*model.Node
	if err := db.Model(model.Node{}).Order("id asc").Find(&nodes).Error; err != nil {
		return nil, err
	}

	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	byPanel := make(map[int][]GraphInbound, len(nodes)+1)
	for _, ib := range inbounds {
		panelId := SelfPanelId
		if ib.NodeID != nil {
			panelId = *ib.NodeID
		}
		byPanel[panelId] = append(byPanel[panelId], GraphInbound{
			Id: ib.Id, Tag: ib.Tag, Remark: ib.Remark, Protocol: string(ib.Protocol),
			Port: ib.Port, Enable: ib.Enable, Clients: len(ib.ClientStats),
		})
	}

	// A cluster registers every master including this one, so the row flagged
	// IsSelf describes the same panel as SelfPanelId. Fold it in rather than
	// drawing one machine as two vertices.
	self := GraphPanel{
		Id: SelfPanelId, Role: model.NodeRoleNode, Status: "online",
		Self: true, Enable: true, Inbounds: byPanel[SelfPanelId],
	}
	panels := make([]GraphPanel, 0, len(nodes)+1)
	for _, n := range nodes {
		if n.IsSelf {
			self.Name = n.Name
			self.Role = n.Role
			self.Address = n.Address
			self.Inbounds = append(self.Inbounds, byPanel[n.Id]...)
			continue
		}
		panels = append(panels, GraphPanel{
			Id: n.Id, Name: n.Name, Role: n.Role, Status: n.Status,
			Address: n.Address, Self: false, Enable: n.Enable,
			Inbounds: byPanel[n.Id],
		})
	}
	if strings.TrimSpace(self.Name) == "" {
		if subDomain, _ := (&SettingService{}).GetSubDomain(); strings.TrimSpace(subDomain) != "" {
			self.Name = subDomain
		} else {
			self.Name = "this panel"
		}
	}
	panels = append([]GraphPanel{self}, panels...)

	var links []*model.CascadeLink
	if err := db.Model(model.CascadeLink{}).Order("id asc").Find(&links).Error; err != nil {
		return nil, err
	}

	filterService := FilterService{}
	rules, err := filterService.Rules()
	if err != nil {
		return nil, err
	}
	lists, err := filterService.Lists()
	if err != nil {
		return nil, err
	}

	return &NetworkGraph{Panels: panels, Links: links, Filters: rules, Lists: lists}, nil
}

// AddLink stores one edge after checking it describes a cascade that can exist:
// both ends must be known panels, the source inbound must live on the source
// panel, and the target inbound on the target panel.
func (s *NetworkService) AddLink(link *model.CascadeLink) error {
	if err := s.validateLink(link); err != nil {
		return err
	}
	link.Applied = 0
	db := database.GetDB()
	wantEnabled := link.Enable
	if err := db.Create(link).Error; err != nil {
		return err
	}
	if wantEnabled {
		return s.markSourceDirty(link.SourcePanelId)
	}
	if err := db.Model(model.CascadeLink{}).Where("id = ?", link.Id).Update("enable", false).Error; err != nil {
		return err
	}
	link.Enable = false
	return s.markSourceDirty(link.SourcePanelId)
}

func (s *NetworkService) DeleteLink(id int) error {
	db := database.GetDB()
	link := &model.CascadeLink{}
	if err := db.Where("id = ?", id).First(link).Error; err != nil {
		return err
	}
	// A filter routes over this edge; without it the rule would stay enabled and
	// forward nowhere, which reads as the filter silently breaking.
	var routed []*model.FilterRule
	if err := db.Model(model.FilterRule{}).Where("cascade_link_id = ?", id).Find(&routed).Error; err != nil {
		return err
	}
	if len(routed) > 0 {
		names := make([]string, 0, len(routed))
		for _, rule := range routed {
			names = append(names, rule.Name)
		}
		return common.NewError("link is still used by filter(s): " + strings.Join(names, ", "))
	}
	if err := db.Where("id = ?", id).Delete(&model.CascadeLink{}).Error; err != nil {
		return err
	}
	return s.markSourceDirty(link.SourcePanelId)
}

func (s *NetworkService) SetLinkEnable(id int, enable bool) error {
	db := database.GetDB()
	link := &model.CascadeLink{}
	if err := db.Where("id = ?", id).First(link).Error; err != nil {
		return err
	}
	if err := db.Model(model.CascadeLink{}).Where("id = ?", id).
		Updates(map[string]any{"enable": enable, "applied": 0}).Error; err != nil {
		return err
	}
	return s.markSourceDirty(link.SourcePanelId)
}

func (s *NetworkService) validateLink(link *model.CascadeLink) error {
	link.SourceInboundTag = strings.TrimSpace(link.SourceInboundTag)
	if link.SourceInboundTag == "" {
		return common.NewError("cascade link needs a source inbound")
	}
	if link.SourcePanelId == link.TargetPanelId {
		return common.NewError("a cascade link must join two different panels")
	}
	if err := s.panelExists(link.SourcePanelId); err != nil {
		return err
	}
	if err := s.panelExists(link.TargetPanelId); err != nil {
		return err
	}

	db := database.GetDB()
	var source model.Inbound
	if err := db.Model(model.Inbound{}).Where("tag = ?", link.SourceInboundTag).First(&source).Error; err != nil {
		return common.NewError("source inbound " + link.SourceInboundTag + " not found")
	}
	if panelOf(&source) != link.SourcePanelId {
		return common.NewError("source inbound " + link.SourceInboundTag + " does not live on the source panel")
	}

	var target model.Inbound
	if err := db.Model(model.Inbound{}).Where("id = ?", link.TargetInboundId).First(&target).Error; err != nil {
		return common.NewError("target inbound not found")
	}
	if panelOf(&target) != link.TargetPanelId {
		return common.NewError("target inbound does not live on the target panel")
	}
	return nil
}

func (s *NetworkService) panelExists(id int) error {
	if id == SelfPanelId {
		return nil
	}
	var count int64
	if err := database.GetDB().Model(model.Node{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return common.NewError("panel not found")
	}
	return nil
}

// markSourceDirty asks the existing node reconciliation to re-push the source
// panel's config; a link on this panel is applied by the local config pipeline
// instead.
func (s *NetworkService) markSourceDirty(panelId int) error {
	if panelId == SelfPanelId {
		return nil
	}
	return (&NodeService{}).MarkNodeDirty(panelId)
}

func panelOf(ib *model.Inbound) int {
	if ib.NodeID == nil {
		return SelfPanelId
	}
	return *ib.NodeID
}
