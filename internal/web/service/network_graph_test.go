package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func initNetworkTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	db := database.GetDB()
	for _, m := range []any{&model.CascadeLink{}, &model.Inbound{}, &model.Node{}} {
		if err := db.Where("1 = 1").Delete(m).Error; err != nil {
			t.Fatalf("clear table: %v", err)
		}
	}
}

func seedGraphInbound(t *testing.T, tag string, port int, nodeID *int) *model.Inbound {
	t.Helper()
	ib := &model.Inbound{
		UserId: 1, Tag: tag, Remark: tag, Enable: true, Port: port, NodeID: nodeID,
		Protocol: model.VLESS, Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("seed inbound %s: %v", tag, err)
	}
	return ib
}

func seedGraphNode(t *testing.T, name, role string) *model.Node {
	t.Helper()
	n := &model.Node{
		Name: name, Scheme: "https", Address: name + ".example.com", Port: 2053,
		BasePath: "/", Role: role, Enable: true, Status: "online",
	}
	if err := database.GetDB().Create(n).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return n
}

// The graph must group inbounds by the ownership column, with panel 0 standing
// for this panel — that mapping is what the editor draws its ports from.
func TestNetworkGraphGroupsInboundsByOwningPanel(t *testing.T) {
	initNetworkTestDB(t)
	node := seedGraphNode(t, "msk1", model.NodeRoleNode)
	seedGraphNode(t, "sibling", model.NodeRoleMaster)
	seedGraphInbound(t, "local-in", 39101, nil)
	seedGraphInbound(t, "remote-in", 39102, &node.Id)

	graph, err := (&NetworkService{}).Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(graph.Panels) != 3 {
		t.Fatalf("panels = %d, want this panel plus two registered ones", len(graph.Panels))
	}

	self := graph.Panels[0]
	if !self.Self || self.Id != SelfPanelId {
		t.Fatalf("first panel = %+v, want this panel at id 0", self)
	}
	if len(self.Inbounds) != 1 || self.Inbounds[0].Tag != "local-in" {
		t.Fatalf("this panel's inbounds = %+v, want only local-in", self.Inbounds)
	}

	var remote GraphPanel
	for _, p := range graph.Panels {
		if p.Id == node.Id {
			remote = p
		}
	}
	if len(remote.Inbounds) != 1 || remote.Inbounds[0].Tag != "remote-in" {
		t.Fatalf("node inbounds = %+v, want only remote-in", remote.Inbounds)
	}
	// A sibling master is a vertex too, so the editor can show the whole cluster.
	if graph.Panels[2].Role != model.NodeRoleMaster {
		t.Fatalf("third panel role = %q, want master", graph.Panels[2].Role)
	}
}

// A cluster registers every master including this one; drawing that row as a
// separate vertex would show one machine twice on the canvas.
func TestNetworkGraphFoldsTheSelfMasterRow(t *testing.T) {
	initNetworkTestDB(t)
	selfRow := seedGraphNode(t, "fi2-helsinki", model.NodeRoleMaster)
	if err := database.GetDB().Model(model.Node{}).Where("id = ?", selfRow.Id).
		Update("is_self", true).Error; err != nil {
		t.Fatalf("mark self: %v", err)
	}
	seedGraphNode(t, "sibling", model.NodeRoleMaster)
	seedGraphInbound(t, "local-in", 39101, nil)

	graph, err := (&NetworkService{}).Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(graph.Panels) != 2 {
		names := make([]string, 0, len(graph.Panels))
		for _, p := range graph.Panels {
			names = append(names, p.Name)
		}
		t.Fatalf("panels = %v, want this panel once plus the sibling", names)
	}
	self := graph.Panels[0]
	if !self.Self || self.Id != SelfPanelId {
		t.Fatalf("first panel = %+v, want this panel", self)
	}
	if self.Name != "fi2-helsinki" || self.Role != model.NodeRoleMaster {
		t.Fatalf("self vertex = %q/%q, want the registered master identity", self.Name, self.Role)
	}
	if len(self.Inbounds) != 1 || self.Inbounds[0].Tag != "local-in" {
		t.Fatalf("self inbounds = %+v, want local-in", self.Inbounds)
	}
	for _, p := range graph.Panels[1:] {
		if p.Self {
			t.Fatalf("panel %q is still flagged as this panel", p.Name)
		}
	}
}

// Every rejection here is an edge that would otherwise be stored and then fail
// silently when the source panel's config is generated.
func TestNetworkAddLinkRejectsImpossibleEdges(t *testing.T) {
	initNetworkTestDB(t)
	node := seedGraphNode(t, "msk1", model.NodeRoleNode)
	local := seedGraphInbound(t, "local-in", 39101, nil)
	remote := seedGraphInbound(t, "remote-in", 39102, &node.Id)
	s := NetworkService{}

	tests := []struct {
		name string
		link model.CascadeLink
		want string
	}{
		{
			name: "same panel on both ends",
			link: model.CascadeLink{SourcePanelId: 0, SourceInboundTag: "local-in", TargetPanelId: 0, TargetInboundId: local.Id},
			want: "two different panels",
		},
		{
			name: "unknown source inbound",
			link: model.CascadeLink{SourcePanelId: node.Id, SourceInboundTag: "nope", TargetPanelId: 0, TargetInboundId: local.Id},
			want: "not found",
		},
		{
			name: "source inbound lives elsewhere",
			link: model.CascadeLink{SourcePanelId: node.Id, SourceInboundTag: "local-in", TargetPanelId: 0, TargetInboundId: local.Id},
			want: "does not live on the source panel",
		},
		{
			name: "target inbound lives elsewhere",
			link: model.CascadeLink{SourcePanelId: node.Id, SourceInboundTag: "remote-in", TargetPanelId: 0, TargetInboundId: remote.Id},
			want: "does not live on the target panel",
		},
		{
			name: "unknown panel",
			link: model.CascadeLink{SourcePanelId: 999, SourceInboundTag: "remote-in", TargetPanelId: 0, TargetInboundId: local.Id},
			want: "panel not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			link := tt.link
			err := s.AddLink(&link)
			if err == nil {
				t.Fatal("AddLink accepted an impossible edge")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestNetworkLinkRoundTrip(t *testing.T) {
	initNetworkTestDB(t)
	node := seedGraphNode(t, "msk1", model.NodeRoleNode)
	local := seedGraphInbound(t, "local-in", 39101, nil)
	seedGraphInbound(t, "remote-in", 39102, &node.Id)
	s := NetworkService{}

	link := &model.CascadeLink{
		Remark: "RU entry to FI exit", SourcePanelId: node.Id, SourceInboundTag: "remote-in",
		TargetPanelId: SelfPanelId, TargetInboundId: local.Id, Enable: true,
	}
	if err := s.AddLink(link); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	if link.Id == 0 {
		t.Fatal("stored link has no id")
	}
	if tag := link.OutboundTag(); tag == "cascade-0" {
		t.Fatalf("outbound tag = %q, want it keyed by the stored id", tag)
	}

	graph, err := s.Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(graph.Links) != 1 || graph.Links[0].SourceInboundTag != "remote-in" {
		t.Fatalf("graph links = %+v, want the stored edge", graph.Links)
	}

	if err := s.DeleteLink(link.Id); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	graph, err = s.Graph()
	if err != nil {
		t.Fatalf("Graph after delete: %v", err)
	}
	if len(graph.Links) != 0 {
		t.Fatalf("graph links = %+v, want none after delete", graph.Links)
	}
}

// A filter node sits on a cascade edge, so deleting that edge underneath it
// would leave a rule pointing at nothing: still enabled, matching nothing,
// forwarding nowhere.
func TestNetworkDeleteLinkRefusedWhileAFilterSitsOnIt(t *testing.T) {
	initNetworkTestDB(t)
	db := database.GetDB()
	for _, m := range []any{&model.FilterRule{}, &model.FilterList{}} {
		if err := db.Where("1 = 1").Delete(m).Error; err != nil {
			t.Fatalf("clear table: %v", err)
		}
	}
	node := seedGraphNode(t, "exit", model.NodeRoleNode)
	seedGraphInbound(t, "entry-in", 39201, nil)
	target := seedGraphInbound(t, "exit-in", 39202, &node.Id)

	s := NetworkService{}
	link := &model.CascadeLink{
		SourcePanelId: SelfPanelId, SourceInboundTag: "entry-in",
		TargetPanelId: node.Id, TargetInboundId: target.Id, Enable: true,
	}
	if err := s.AddLink(link); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	filters := FilterService{}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"ads.example.com"}, Enable: true}
	if err := filters.CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	rule := &model.FilterRule{
		Name: "ads to exit", PanelId: SelfPanelId, ListIds: []int{list.Id},
		Action: model.FilterActionCascade, CascadeLinkId: link.Id, Enable: true,
	}
	if err := filters.CreateRule(rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	err := s.DeleteLink(link.Id)
	if err == nil {
		t.Fatal("DeleteLink removed an edge a filter still routes over")
	}
	if !strings.Contains(err.Error(), "ads to exit") {
		t.Fatalf("error = %q, want it to name the filter", err.Error())
	}
	var still int64
	if err := db.Model(model.CascadeLink{}).Where("id = ?", link.Id).Count(&still).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if still != 1 {
		t.Fatal("the refused delete removed the link anyway")
	}

	if err := filters.DeleteRule(rule.Id); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if err := s.DeleteLink(link.Id); err != nil {
		t.Fatalf("DeleteLink after the filter is gone: %v", err)
	}
}
