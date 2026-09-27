package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A cascade and a filter the master draws for an entry node have to reach
// that node's own config; before, only rules owned by the master itself were
// ever rendered.
func TestManagedRoutingReachesTheEntryNode(t *testing.T) {
	initFilterTestDB(t)
	exit, target := seedCascadeTarget(t, "exit.example.com")
	db := database.GetDB()
	entry := &model.Node{
		Name: "ru7", Scheme: "https", Address: "ru7.example.com", Port: 1367, BasePath: "/",
		Role: model.NodeRoleNode, Enable: true, Status: "online",
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Inbound{
		UserId: 1, Tag: "n-ru7-de", Enable: true, Port: 2053, NodeID: &entry.Id,
		Protocol: model.VLESS, Settings: `{"clients":[]}`, StreamSettings: `{"network":"ws"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"geosite:category-ads-all"}, Enable: true}
	db.Create(list)
	link := &model.CascadeLink{SourcePanelId: entry.Id, SourceInboundTag: "n-ru7-de", TargetPanelId: exit.Id, TargetInboundId: target.Id, Enable: true}
	db.Create(link)
	mine := &model.FilterRule{Name: "block ads", PanelId: entry.Id, ListIds: []int{list.Id}, Action: model.FilterActionBlock, Enable: true}
	other := &model.FilterRule{Name: "master's own", PanelId: SelfPanelId, ListIds: []int{list.Id}, Action: model.FilterActionBlock, Enable: true}
	db.Create(mine)
	db.Create(other)

	var pushes []*ManagedRouting
	old := managedRoutingPusher
	managedRoutingPusher = func(_ context.Context, n *model.Node, mr *ManagedRouting) error {
		if n.Id != entry.Id {
			t.Errorf("pushed to node %d", n.Id)
		}
		pushes = append(pushes, mr)
		return nil
	}
	t.Cleanup(func() {
		managedRoutingPusher = old
		routingPushed = map[int]string{}
		routingFailedAt = map[int]time.Time{}
	})

	SyncManagedRouting()
	if len(pushes) != 1 {
		t.Fatalf("pushes = %d, want 1 (only the online entry node)", len(pushes))
	}
	mr := pushes[0]
	joined := string(mustJSON(t, mr.Rules))
	if len(mr.Outbounds) != 1 || !strings.Contains(string(mr.Outbounds[0]), "exit.example.com") ||
		len(mr.Rules) != 2 || !strings.Contains(joined, `"n-ru7-de"`) || !strings.Contains(joined, "category-ads") {
		t.Fatalf("share = outbounds %s rules %s", mustJSON(t, mr.Outbounds), joined)
	}

	SyncManagedRouting()
	if len(pushes) != 1 {
		t.Fatal("an unchanged share was pushed again")
	}
	var got model.FilterRule
	db.First(&got, mine.Id)
	if got.Applied == 0 {
		t.Fatal("the node's rule is not marked applied after the push")
	}
	var master model.FilterRule
	db.First(&master, other.Id)
	if master.Applied != 0 {
		t.Fatal("the master's own rule was marked by a node push")
	}
	db.Model(mine).Update("enable", false)
	SyncManagedRouting()
	if len(pushes) != 2 || len(pushes[1].Rules) != 1 {
		t.Fatalf("a changed share must be pushed: %d pushes", len(pushes))
	}
}

func TestInjectManagedRoutingOnTheNode(t *testing.T) {
	initFilterTestDB(t)
	var xs XrayService
	mr := &ManagedRouting{
		Version: "v1",
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"tag":"cascade-1","protocol":"vless"}`),
			json.RawMessage(`{"tag":"direct","protocol":"freedom","settings":{"from":"master"}}`),
		},
		Rules: []json.RawMessage{
			json.RawMessage(`{"type":"field","inboundTag":["in-a"],"outboundTag":"cascade-1"}`),
			json.RawMessage(`{"type":"field","inboundTag":["in-a"],"outboundTag":"missing"}`),
		},
	}
	if changed, err := xs.StoreManagedRouting(mr); err != nil || !changed {
		t.Fatalf("store: %v %v", changed, err)
	}
	if changed, _ := xs.StoreManagedRouting(mr); changed {
		t.Fatal("the same version must not count as a change")
	}
	cfg := clusterTestConfig(t)
	injectManagedRouting(cfg)
	out := string(cfg.OutboundConfigs)
	if !strings.Contains(out, `"cascade-1"`) || strings.Contains(out, "master") {
		t.Fatalf("outbounds = %s (the node's own direct must win)", out)
	}
	rules := routingRules(t, cfg)
	if len(rules) != 2 || rules[0]["outboundTag"] != "cascade-1" || rules[1]["outboundTag"] != "api" {
		t.Fatalf("rules = %v, want the share's usable rule ahead of the template's", rules)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
