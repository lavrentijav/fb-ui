package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A template taken from one entry node lands on another with that node's own
// name in its certificate path and SNI, and with the source's clients.
func TestInboundTemplateCopiesAnEntryToAnotherPanel(t *testing.T) {
	db := initFlowTestDB(t)
	ibSvc := &InboundService{}
	src := &model.Inbound{
		Tag: "ru4-de", Enable: true, Port: 2053, Protocol: model.VLESS, Remark: "DE",
		StreamSettings: `{"network":"ws","security":"tls","tlsSettings":{"serverName":"ru.walk.example.com",` +
			`"certificates":[{"certificateFile":"/root/cert/ru4.walk.example.com/fullchain.pem","keyFile":"/root/cert/ru4.walk.example.com/privkey.pem"}]}}`,
		Settings: `{"clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"alice","subId":"s1","enable":true},` +
			`{"id":"22222222-2222-4222-8222-222222222222","email":"bob","subId":"s2","enable":true}],"decryption":"none"}`,
		ShareAddrStrategy: "custom", ShareAddr: "ru.walk.example.com",
	}
	if _, _, err := ibSvc.AddInbound(src); err != nil {
		t.Fatal(err)
	}
	ru4 := &model.Node{Name: "ru4", Address: "ru4.walk.example.com", Scheme: "https", Port: 1367, Enable: true}
	self := &model.Node{Name: "ru7", Address: "ru7.walk.example.com", Scheme: "https", Port: 1367, Enable: true, IsSelf: true, Role: model.NodeRoleMaster}
	for _, n := range []*model.Node{ru4, self} {
		if err := db.Create(n).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.Inbound{}).Where("id = ?", src.Id).Update("node_id", ru4.Id).Error; err != nil {
		t.Fatal(err)
	}

	var svc InboundTemplateService
	tpl, err := svc.FromInbounds(&InboundTemplateFromInbounds{Name: "RU entry", InboundIds: []int{src.Id}})
	if err != nil {
		t.Fatal(err)
	}
	it := tpl.Items[0]
	if strings.Contains(it.StreamSettings, "ru4.walk") || !strings.Contains(it.StreamSettings, "/root/cert/{{NODE_DOMAIN}}/fullchain.pem") ||
		strings.Contains(it.Settings, "alice") || it.ShareAddr != "ru.walk.example.com" {
		t.Fatalf("template item = %+v", it)
	}

	res, err := svc.Apply(tpl.Id, &InboundTemplateApply{NodeIds: []int{SelfPanelId}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Created) != 1 || res[0].Attached != 2 || len(res[0].Errors) != 0 {
		t.Fatalf("apply = %+v", res)
	}
	var made model.Inbound
	if err := db.Where("(node_id IS NULL OR node_id = 0) AND port = ?", 2053).First(&made).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(made.StreamSettings, "/root/cert/ru7.walk.example.com/privkey.pem") {
		t.Fatalf("node domain not substituted: %s", made.StreamSettings)
	}
	clients, _ := ibSvc.clientService.ListForInbound(nil, made.Id)
	var emails []string
	for _, c := range clients {
		emails = append(emails, c.Email)
	}
	slices.Sort(emails)
	if !slices.Equal(emails, []string{"alice", "bob"}) {
		t.Fatalf("attached clients = %v", emails)
	}

	again, err := svc.Apply(tpl.Id, &InboundTemplateApply{NodeIds: []int{SelfPanelId}}, 1)
	if err != nil || len(again[0].Created) != 0 || len(again[0].Skipped) != 1 {
		t.Fatalf("second apply must skip the existing inbound: %+v, %v", again, err)
	}
}
