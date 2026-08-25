package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func clusterTestConfig(t *testing.T) *xray.Config {
	t.Helper()
	return &xray.Config{
		OutboundConfigs: json_util.RawMessage(
			`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`,
		),
		RouterConfig: json_util.RawMessage(
			`{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]}`,
		),
	}
}

func routingRules(t *testing.T, cfg *xray.Config) []map[string]any {
	t.Helper()
	routing := map[string]any{}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatalf("decode routing: %v", err)
	}
	raw, _ := routing["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		rule, _ := entry.(map[string]any)
		out = append(out, rule)
	}
	return out
}

func matcherStrings(t *testing.T, rule map[string]any, field string) []string {
	t.Helper()
	raw, _ := rule[field].([]any)
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		s, _ := value.(string)
		out = append(out, s)
	}
	return out
}

func clusterLists() []*model.FilterList {
	return []*model.FilterList{
		{Id: 1, Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"geosite:category-ads-all"}, Enable: true},
		{Id: 2, Name: "ru", Kind: model.FilterKindIP, Entries: []string{"geoip:ru"}, Enable: true},
		{Id: 3, Name: "off", Kind: model.FilterKindDomain, Entries: []string{"example.com"}, Enable: false},
	}
}

// The layers are one chain and Xray takes the first match, so they have to
// reach the config in their stored order and ahead of the template's own rules.
func TestClusterRoutingEmitsTheChainInOrder(t *testing.T) {
	cfg := clusterTestConfig(t)
	rules := []*model.FilterRule{
		{Id: 1, Name: "block ads", PanelId: SelfPanelId, SourceInboundTags: []string{"in-a"}, ListIds: []int{1}, Action: model.FilterActionBlock, Enable: true},
		{Id: 2, Name: "ru direct", PanelId: SelfPanelId, SourceInboundTags: []string{"in-a"}, ListIds: []int{2}, Action: model.FilterActionDirect, Enable: true},
	}

	applied, _ := injectClusterRouting(cfg, rules, clusterLists(), nil, []string{"in-a"})
	if len(applied) != 2 || applied[0] != 1 || applied[1] != 2 {
		t.Fatalf("applied = %v, want both rules in order", applied)
	}

	emitted := routingRules(t, cfg)
	if len(emitted) != 3 {
		t.Fatalf("rules = %d, want the two layers plus the template's own", len(emitted))
	}
	if emitted[0]["outboundTag"] != "blocked" {
		t.Fatalf("first rule targets %v, want blocked", emitted[0]["outboundTag"])
	}
	if got := matcherStrings(t, emitted[0], "domain"); len(got) != 1 || got[0] != "geosite:category-ads-all" {
		t.Fatalf("domain matchers = %v", got)
	}
	if emitted[1]["outboundTag"] != "direct" {
		t.Fatalf("second rule targets %v, want direct", emitted[1]["outboundTag"])
	}
	if got := matcherStrings(t, emitted[1], "ip"); len(got) != 1 || got[0] != "geoip:ru" {
		t.Fatalf("ip matchers = %v", got)
	}
	// The template's api rule must still be there, after ours.
	if got := matcherStrings(t, emitted[2], "inboundTag"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("template rule = %v, want the api rule kept", emitted[2])
	}
}

// A field rule with no matchers matches everything, so a rule whose lists are
// all disabled or empty must never reach the config: as a block it would
// black-hole the whole panel.
func TestClusterRoutingSkipsARuleWithNoMatchers(t *testing.T) {
	cfg := clusterTestConfig(t)
	rules := []*model.FilterRule{
		{Id: 1, Name: "empty", PanelId: SelfPanelId, ListIds: []int{3}, Action: model.FilterActionBlock, Enable: true},
	}
	if applied, _ := injectClusterRouting(cfg, rules, clusterLists(), nil, []string{"in-a"}); len(applied) != 0 {
		t.Fatalf("applied = %v, want the matcher-less rule skipped", applied)
	}
	if got := len(routingRules(t, cfg)); got != 1 {
		t.Fatalf("rules = %d, want the config untouched", got)
	}
}

// A rule without explicit tags means "every inbound this panel owns" — which is
// not the same as every inbound in the config, where the api bridge also lives.
func TestClusterRoutingScopesAPanelWideRuleToOwnInbounds(t *testing.T) {
	cfg := clusterTestConfig(t)
	rules := []*model.FilterRule{
		{Id: 1, Name: "panel wide", PanelId: SelfPanelId, ListIds: []int{1}, Action: model.FilterActionBlock, Enable: true},
	}
	injectClusterRouting(cfg, rules, clusterLists(), nil, []string{"in-a", "in-b"})

	tags := matcherStrings(t, routingRules(t, cfg)[0], "inboundTag")
	if len(tags) != 2 || tags[0] != "in-a" || tags[1] != "in-b" {
		t.Fatalf("inboundTag = %v, want the panel's own inbounds", tags)
	}
	for _, tag := range tags {
		if tag == "api" {
			t.Fatal("a panel-wide rule captured the api inbound")
		}
	}
}

func TestClusterRoutingLeavesForeignAndUnbackedRulesPending(t *testing.T) {
	links := []*model.CascadeLink{{Id: 7, Enable: true}}
	tests := []struct {
		name string
		rule *model.FilterRule
	}{
		{
			name: "another panel's rule",
			rule: &model.FilterRule{Id: 1, Name: "on a node", PanelId: 4, ListIds: []int{1}, Action: model.FilterActionBlock, Enable: true},
		},
		{
			name: "disabled rule",
			rule: &model.FilterRule{Id: 2, Name: "paused", PanelId: SelfPanelId, ListIds: []int{1}, Action: model.FilterActionBlock},
		},
		{
			name: "cascade with no outbound yet",
			rule: &model.FilterRule{Id: 3, Name: "to exit", PanelId: SelfPanelId, ListIds: []int{1}, Action: model.FilterActionCascade, CascadeLinkId: 7, Enable: true},
		},
		{
			name: "cascade naming a link that is gone",
			rule: &model.FilterRule{Id: 4, Name: "orphan", PanelId: SelfPanelId, ListIds: []int{1}, Action: model.FilterActionCascade, CascadeLinkId: 99, Enable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clusterTestConfig(t)
			applied, _ := injectClusterRouting(cfg, []*model.FilterRule{tt.rule}, clusterLists(), links, []string{"in-a"})
			if len(applied) != 0 {
				t.Fatalf("applied = %v, want it left pending", applied)
			}
			if got := len(routingRules(t, cfg)); got != 1 {
				t.Fatalf("rules = %d, want the config untouched", got)
			}
		})
	}
}

// Once the cascade outbound exists, the layer routes onto it by its own tag.
func TestClusterRoutingUsesTheCascadeOutboundWhenItExists(t *testing.T) {
	cfg := clusterTestConfig(t)
	cfg.OutboundConfigs = json_util.RawMessage(
		`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"},{"tag":"cascade-7","protocol":"vless"}]`,
	)
	link := &model.CascadeLink{Id: 7, Enable: true}
	rule := &model.FilterRule{
		Id: 3, Name: "to exit", PanelId: SelfPanelId, SourceInboundTags: []string{"in-a"},
		ListIds: []int{1}, Action: model.FilterActionCascade, CascadeLinkId: link.Id, Enable: true,
	}

	applied, _ := injectClusterRouting(cfg, []*model.FilterRule{rule}, clusterLists(), []*model.CascadeLink{link}, []string{"in-a"})
	if len(applied) != 1 || applied[0] != 3 {
		t.Fatalf("applied = %v, want the cascade layer", applied)
	}
	if got := routingRules(t, cfg)[0]["outboundTag"]; got != "cascade-7" {
		t.Fatalf("outboundTag = %v, want the link's own tag", got)
	}
}

// The whole path: a stored chain has to come out of the config generator, and
// "applied" may only be written once that config is the one being run.
func TestGeneratedConfigCarriesTheFilterChain(t *testing.T) {
	initFilterTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Inbound{}).Error; err != nil {
		t.Fatalf("clear inbounds: %v", err)
	}
	inbound := &model.Inbound{
		UserId: 1, Tag: "in-local", Remark: "local", Enable: true, Port: 34567,
		Protocol: model.VLESS, Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}

	filters := FilterService{}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"ads.example.com"}, Enable: true}
	if err := filters.CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	rule := &model.FilterRule{
		Name: "block ads", PanelId: SelfPanelId, SourceInboundTags: []string{"in-local"},
		ListIds: []int{list.Id}, Action: model.FilterActionBlock, Enable: true,
	}
	if err := filters.CreateRule(rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	s := &XrayService{}
	cfg, err := s.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	emitted := routingRules(t, cfg)
	if len(emitted) == 0 || emitted[0]["outboundTag"] != "blocked" {
		t.Fatalf("first routing rule = %+v, want the block layer", emitted)
	}
	if got := matcherStrings(t, emitted[0], "domain"); len(got) != 1 || got[0] != "ads.example.com" {
		t.Fatalf("domain matchers = %v", got)
	}

	stored, err := filters.RuleById(rule.Id)
	if err != nil {
		t.Fatalf("RuleById: %v", err)
	}
	if stored.Applied != 0 {
		t.Fatal("generating a config marked the layer applied before it was running")
	}

	if err := markClusterApplied(s.materializedRules, s.materializedLinks); err != nil {
		t.Fatalf("markClusterApplied: %v", err)
	}
	if stored, err = filters.RuleById(rule.Id); err != nil {
		t.Fatalf("RuleById: %v", err)
	}
	if stored.Applied == 0 {
		t.Fatal("the layer in the running config is still pending")
	}

	// A layer that stops reaching the config goes back to pending.
	if err := markClusterApplied(nil, nil); err != nil {
		t.Fatalf("markClusterApplied(nil, nil): %v", err)
	}
	if stored, err = filters.RuleById(rule.Id); err != nil {
		t.Fatalf("RuleById: %v", err)
	}
	if stored.Applied != 0 {
		t.Fatal("a layer that left the config kept a stale applied mark")
	}
}

// A link is what happens to the traffic of its inbound that no layer claimed,
// so it has to be a rule of its own — and it has to come after the layers.
func TestClusterRoutingEmitsLinksAfterTheLayers(t *testing.T) {
	cfg := clusterTestConfig(t)
	cfg.OutboundConfigs = json_util.RawMessage(
		`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"},{"tag":"to-fi2","protocol":"vless"}]`,
	)
	rules := []*model.FilterRule{
		{Id: 1, Name: "block ads", PanelId: SelfPanelId, SourceInboundTags: []string{"in-a"}, ListIds: []int{1}, Action: model.FilterActionBlock, Enable: true},
	}
	links := []*model.CascadeLink{
		{Id: 4, SourcePanelId: SelfPanelId, SourceInboundTag: "in-a", OutboundTag: "to-fi2", Enable: true},
	}

	appliedRules, appliedLinks := injectClusterRouting(cfg, rules, clusterLists(), links, []string{"in-a"})
	if len(appliedRules) != 1 || len(appliedLinks) != 1 || appliedLinks[0] != 4 {
		t.Fatalf("applied rules=%v links=%v, want both", appliedRules, appliedLinks)
	}

	emitted := routingRules(t, cfg)
	if emitted[0]["outboundTag"] != "blocked" {
		t.Fatalf("first rule = %+v, want the layer to win", emitted[0])
	}
	if emitted[1]["outboundTag"] != "to-fi2" {
		t.Fatalf("second rule = %+v, want the link", emitted[1])
	}
	if got := matcherStrings(t, emitted[1], "inboundTag"); len(got) != 1 || got[0] != "in-a" {
		t.Fatalf("link rule watches %v, want its source inbound", got)
	}
	if _, hasMatchers := emitted[1]["domain"]; hasMatchers {
		t.Fatal("the link rule carries matchers; it is meant to take the rest")
	}
}

func TestClusterRoutingLeavesLinksWithoutAnOutboundPending(t *testing.T) {
	tests := []struct {
		name string
		link *model.CascadeLink
	}{
		{
			name: "outbound not in this config",
			link: &model.CascadeLink{Id: 4, SourcePanelId: SelfPanelId, SourceInboundTag: "in-a", OutboundTag: "missing", Enable: true},
		},
		{
			// Nothing generates the outbound yet, so an unnamed one is pending.
			name: "left for the panel to generate",
			link: &model.CascadeLink{Id: 5, SourcePanelId: SelfPanelId, SourceInboundTag: "in-a", Enable: true},
		},
		{
			name: "another panel's link",
			link: &model.CascadeLink{Id: 6, SourcePanelId: 3, SourceInboundTag: "in-a", OutboundTag: "direct", Enable: true},
		},
		{
			name: "paused link",
			link: &model.CascadeLink{Id: 7, SourcePanelId: SelfPanelId, SourceInboundTag: "in-a", OutboundTag: "direct"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clusterTestConfig(t)
			_, appliedLinks := injectClusterRouting(cfg, nil, clusterLists(), []*model.CascadeLink{tt.link}, []string{"in-a"})
			if len(appliedLinks) != 0 {
				t.Fatalf("applied = %v, want it left pending", appliedLinks)
			}
			if got := len(routingRules(t, cfg)); got != 1 {
				t.Fatalf("rules = %d, want the config untouched", got)
			}
		})
	}
}

// A layer that names its link routes onto the outbound that link points at.
func TestClusterRoutingCascadeLayerFollowsTheLinksOutbound(t *testing.T) {
	cfg := clusterTestConfig(t)
	cfg.OutboundConfigs = json_util.RawMessage(
		`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"},{"tag":"to-fi2","protocol":"vless"}]`,
	)
	link := &model.CascadeLink{Id: 4, SourcePanelId: SelfPanelId, SourceInboundTag: "in-a", OutboundTag: "to-fi2", Enable: true}
	rule := &model.FilterRule{
		Id: 1, Name: "ru to exit", PanelId: SelfPanelId, SourceInboundTags: []string{"in-a"},
		ListIds: []int{2}, Action: model.FilterActionCascade, CascadeLinkId: link.Id, Enable: true,
	}

	appliedRules, _ := injectClusterRouting(cfg, []*model.FilterRule{rule}, clusterLists(), []*model.CascadeLink{link}, []string{"in-a"})
	if len(appliedRules) != 1 {
		t.Fatalf("applied = %v, want the cascade layer", appliedRules)
	}
	if got := routingRules(t, cfg)[0]["outboundTag"]; got != "to-fi2" {
		t.Fatalf("outboundTag = %v, want the link's outbound", got)
	}
}
