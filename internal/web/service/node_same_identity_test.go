package service

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// ru2 taking over ru4: ru4's clients are ru2's own clients (same email, same
// UUID) and must attach to ru4's inbounds; an email that only shares the name
// with a different credential is still a clash and stays out.
func TestSetRemoteTraffic_AdoptsTheSameClientServedByAnotherNode(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	db := database.GetDB()

	const same, clash, fresh = "vless_same", "vless_clash", "trojan_fresh"
	const uid, otherUID = "11111111-2222-3333-4444-555555555555", "99999999-8888-7777-6666-555555555555"
	local := &model.Inbound{
		UserId: 1, Tag: "in-12053-tcp", Enable: true, Port: 12053, Protocol: model.VLESS,
		Settings: `{"clients":[{"email":"` + same + `","id":"` + uid + `"},{"email":"` + clash + `","id":"` + uid + `"}]}`,
	}
	if err := db.Create(local).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range []*model.ClientRecord{{Email: same, UUID: uid, Enable: true}, {Email: clash, UUID: uid, Enable: true}, {Email: fresh, UUID: uid, Enable: true}} {
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: c.Id, InboundId: local.Id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	nodeID := 4
	nodeSettings := `{"clients":[{"email":"` + same + `","id":"` + uid + `","enable":true},{"email":"` + clash + `","id":"` + otherUID + `","enable":true},` +
		`{"email":"` + fresh + `","password":"never-stored-here","enable":true}]}`
	central := &model.Inbound{UserId: 1, NodeID: &nodeID, Tag: "n4-in-12053-tcp", Enable: true, Port: 12053, Protocol: model.VLESS, Settings: nodeSettings}
	if err := db.Create(central).Error; err != nil {
		t.Fatal(err)
	}
	snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{
		{Tag: "n4-in-12053-tcp", Enable: true, Port: 12053, Protocol: model.VLESS, Settings: nodeSettings},
	}}
	if _, err := (&InboundService{}).setRemoteTrafficLocked(nodeID, snap, false, false); err != nil {
		t.Fatal(err)
	}
	var attached []string
	db.Table("clients").Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Where("client_inbounds.inbound_id = ?", central.Id).Pluck("clients.email", &attached)
	slices.Sort(attached)
	if !slices.Equal(attached, []string{fresh, same}) {
		t.Fatalf("attached to ru4's inbound = %v, want %q and %q", attached, fresh, same)
	}
}
