package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedCascadeTarget(t *testing.T, nodeAddress string) (*model.Node, *model.Inbound) {
	t.Helper()
	db := database.GetDB()
	for _, m := range []any{&model.CascadeLink{}, &model.Inbound{}, &model.Node{}, &model.ClientRecord{}} {
		if err := db.Where("1 = 1").Delete(m).Error; err != nil {
			t.Fatalf("clear table: %v", err)
		}
	}
	node := &model.Node{
		Name: "exit", Scheme: "https", Address: nodeAddress, Port: 2053,
		BasePath: "/", Role: model.NodeRoleNode, Enable: true,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	inbound := &model.Inbound{
		UserId: 1, Tag: "exit-in", Enable: true, Port: 40443, NodeID: &node.Id,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"exit.example.com","settings":{"fingerprint":"chrome"},` +
			`"certificates":[{"certificateFile":"/etc/ssl/cert.pem","keyFile":"/etc/ssl/key.pem"}]}}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{
		{ID: "11111111-2222-3333-4444-555555555555", Email: "exit@lab", Enable: true},
	}); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	return node, inbound
}

// The cascade dials the target the same way a subscription client would, so the
// outbound must carry the target panel's address and the client's credential —
// and none of the inbound's server-side material.
func TestCascadeOutboundDialsTheTargetInbound(t *testing.T) {
	initFilterTestDB(t)
	node, inbound := seedCascadeTarget(t, "exit.example.com")

	link := &model.CascadeLink{
		Id: 1, SourcePanelId: SelfPanelId, SourceInboundTag: "entry-in",
		TargetPanelId: node.Id, TargetInboundId: inbound.Id, Enable: true,
	}
	built, ok := buildCascadeOutbound(link)
	if !ok {
		t.Fatal("no outbound built for a complete link")
	}
	var out map[string]any
	if err := json.Unmarshal(built, &out); err != nil {
		t.Fatalf("decode outbound: %v", err)
	}
	if out["protocol"] != "vless" || out["tag"] != "cascade-1" {
		t.Fatalf("outbound = %+v, want a vless outbound tagged after its link", out)
	}
	settings, _ := out["settings"].(map[string]any)
	if settings["address"] != "exit.example.com" || settings["port"] != float64(40443) {
		t.Fatalf("settings = %+v, want the target panel's address and the inbound port", settings)
	}
	if settings["id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("settings = %+v, want the target client's id", settings)
	}
	if settings["encryption"] != "none" {
		t.Fatalf("encryption = %v, want the inbound's decryption value", settings["encryption"])
	}
	stream, _ := out["streamSettings"].(map[string]any)
	tls, _ := stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "exit.example.com" || tls["fingerprint"] != "chrome" {
		t.Fatalf("tlsSettings = %+v, want the dialer's view", tls)
	}
	if _, leaked := tls["certificates"]; leaked {
		t.Fatal("the target's certificates leaked into the cascade outbound")
	}
}

func TestCascadeOutboundRefusesAnIncompleteLink(t *testing.T) {
	initFilterTestDB(t)
	node, inbound := seedCascadeTarget(t, "exit.example.com")

	tests := []struct {
		name string
		link *model.CascadeLink
	}{
		{
			name: "target inbound is gone",
			link: &model.CascadeLink{Id: 2, TargetPanelId: node.Id, TargetInboundId: 4242, Enable: true},
		},
		{
			name: "target inbound moved to another panel",
			link: &model.CascadeLink{Id: 3, TargetPanelId: 999, TargetInboundId: inbound.Id, Enable: true},
		},
		{
			name: "no such client on the target",
			link: &model.CascadeLink{Id: 4, TargetPanelId: node.Id, TargetInboundId: inbound.Id, TargetClientEmail: "ghost@lab", Enable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := buildCascadeOutbound(tt.link); ok {
				t.Fatal("built an outbound for a link that cannot be dialed")
			}
		})
	}
}

// End to end: a link with no tag of its own gets a generated outbound, and the
// routing pass then finds it and stops leaving the link pending.
func TestGeneratedConfigCarriesTheCascadeOutbound(t *testing.T) {
	initFilterTestDB(t)
	node, inbound := seedCascadeTarget(t, "exit.example.com")
	entry := &model.Inbound{
		UserId: 1, Tag: "entry-in", Enable: true, Port: 40080, Protocol: model.VLESS,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := database.GetDB().Create(entry).Error; err != nil {
		t.Fatalf("seed entry inbound: %v", err)
	}
	network := NetworkService{}
	link := &model.CascadeLink{
		SourcePanelId: SelfPanelId, SourceInboundTag: "entry-in",
		TargetPanelId: node.Id, TargetInboundId: inbound.Id, Enable: true,
	}
	if err := network.AddLink(link); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	s := &XrayService{}
	cfg, err := s.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatalf("decode outbounds: %v", err)
	}
	var found map[string]any
	for _, out := range outbounds {
		if out["tag"] == link.GeneratedOutboundTag() {
			found = out
		}
	}
	if found == nil {
		t.Fatalf("outbound tags = %v, want the generated cascade one", outboundTags(outbounds))
	}
	if len(s.materializedLinks) != 1 || s.materializedLinks[0] != link.Id {
		t.Fatalf("materialized links = %v, want the link routed onto its outbound", s.materializedLinks)
	}
	first := routingRules(t, cfg)[0]
	if first["outboundTag"] != link.GeneratedOutboundTag() {
		t.Fatalf("first routing rule = %+v, want it to target the generated outbound", first)
	}
}

func outboundTags(outbounds []map[string]any) []string {
	tags := make([]string, 0, len(outbounds))
	for _, out := range outbounds {
		tag, _ := out["tag"].(string)
		tags = append(tags, tag)
	}
	return tags
}
