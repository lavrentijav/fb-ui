package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// A cascade outbound dials its target inbound through that inbound's host
// override, and is only built when the source's config is. Changing the host
// must therefore rebuild every cascade that dials the inbound — otherwise the
// source keeps dialling the old address (seen live: NL moved to a new exit,
// the host was added after the link, and the entry kept dialling the exit's
// loopback port until the link was saved again).
func TestHostChangeRebuildsCascadesThatDialIt(t *testing.T) {
	initNetworkTestDB(t)
	if err := database.GetDB().Where("1 = 1").Delete(&model.Host{}).Error; err != nil {
		t.Fatalf("clear hosts: %v", err)
	}
	entry := seedGraphNode(t, "ru4", model.NodeRoleNode)
	exit := seedGraphNode(t, "nl2", model.NodeRoleNode)
	seedGraphInbound(t, "n-entry", 12096, &entry.Id)
	target := seedGraphInbound(t, "n-exit", 12443, &exit.Id)
	seedGraphInbound(t, "self-entry", 12097, nil)
	other := seedGraphInbound(t, "n-other", 12444, &exit.Id)

	net := NetworkService{}
	for _, l := range []*model.CascadeLink{
		{Remark: "ru4 NL", SourcePanelId: entry.Id, SourceInboundTag: "n-entry", TargetPanelId: exit.Id, TargetInboundId: target.Id, Enable: true},
		{Remark: "self NL", SourcePanelId: SelfPanelId, SourceInboundTag: "self-entry", TargetPanelId: exit.Id, TargetInboundId: target.Id, Enable: true},
	} {
		if err := net.AddLink(l); err != nil {
			t.Fatalf("AddLink %s: %v", l.Remark, err)
		}
	}
	settle := func() {
		t.Helper()
		db := database.GetDB()
		if err := db.Model(&model.Node{}).Where("1 = 1").Update("config_dirty", false).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.CascadeLink{}).Where("1 = 1").Update("applied", 1).Error; err != nil {
			t.Fatal(err)
		}
		(&XrayService{}).IsNeedRestartAndSetFalse()
	}
	entryDirty := func() bool {
		t.Helper()
		n := &model.Node{}
		if err := database.GetDB().Where("id = ?", entry.Id).First(n).Error; err != nil {
			t.Fatal(err)
		}
		return n.ConfigDirty
	}
	hosts := &HostService{}
	group := func(inboundId int, addr string) *entity.HostGroup {
		return &entity.HostGroup{InboundIds: []int{inboundId}, Remark: addr, Hosts: []string{addr + ":443"}, Security: "tls", Sni: addr}
	}

	settle()
	created, err := hosts.AddHostGroup(group(target.Id, "nl2.example.com"))
	if err != nil {
		t.Fatalf("AddHostGroup: %v", err)
	}
	if !entryDirty() {
		t.Fatal("adding the exit's host left the entry node's config as it was")
	}
	if !(&XrayService{}).IsNeedRestartAndSetFalse() {
		t.Fatal("adding the exit's host did not rebuild this panel's own cascade")
	}

	groupId := created[0].GroupId
	for _, step := range []struct {
		name   string
		change func() error
	}{
		{"update", func() error {
			_, err := hosts.UpdateHostGroup(groupId, group(target.Id, "nl3.example.com"))
			return err
		}},
		{"disable", func() error { return hosts.SetHostGroupEnable(groupId, false) }},
		{"delete", func() error { return hosts.DeleteHostGroup(groupId) }},
	} {
		name, change := step.name, step.change
		settle()
		if err := change(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !entryDirty() {
			t.Fatalf("%s of the exit's host left the entry node's config as it was", name)
		}
	}

	settle()
	if _, err := hosts.AddHostGroup(group(other.Id, "other.example.com")); err != nil {
		t.Fatalf("AddHostGroup other: %v", err)
	}
	if entryDirty() || (&XrayService{}).IsNeedRestartAndSetFalse() {
		t.Fatal("a host on an inbound no cascade dials rebuilt cascades anyway")
	}
}
