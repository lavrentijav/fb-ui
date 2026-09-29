package sub

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// The RU cascade entries are one inbound per node, all shared under one name,
// so a client attached to every node's copy used to get the same link once
// per node.
func TestSubDedupesCascadeCopies(t *testing.T) {
	seedSubDB(t)
	first := seedSubInbound(t, "s1", "ru2-de", 2053, 1, wsTLSStream)
	db := database.GetDB()
	var client model.ClientRecord
	if err := db.Where("sub_id = ?", "s1").First(&client).Error; err != nil {
		t.Fatal(err)
	}
	for i, tag := range []string{"ru4-de", "ru6-de"} {
		node := 20 + i
		copyIb := *first
		copyIb.Id, copyIb.Tag, copyIb.NodeID = 0, tag, &node
		if err := db.Create(&copyIb).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: copyIb.Id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedSubInbound(t, "s1", "ru2-fi", 2083, 2, wsTLSStream)

	links, _, _, _, err := NewSubService("").GetSubs("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.Join(links, "\n"), "\n")
	if len(parts) != 2 || !strings.Contains(parts[0], ":2053") || !strings.Contains(parts[1], ":2083") {
		t.Fatalf("want one link per distinct endpoint, got %d:\n%s", len(parts), strings.Join(parts, "\n"))
	}
}

func TestDedupeLinksIgnoresOnlyTheName(t *testing.T) {
	vm := func(ps, port string) string {
		return "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"a.example.com","port":"`+port+`","ps":"`+ps+`"}`))
	}
	in := []string{
		"vless://u@a.example.com:443?type=ws#DE",
		"vless://u@a.example.com:443?type=ws#DE copy\nvless://u@b.example.com:443?type=ws#DE",
		vm("one", "443"), vm("two", "443"), vm("three", "8443"),
		"vless://u@a.example.com:443?type=ws&encryption=none#DE old port",
		"vless://u@a.example.com:443?encryption=none&type=grpc#DE grpc",
	}
	want := []string{
		"vless://u@a.example.com:443?type=ws#DE", "vless://u@b.example.com:443?type=ws#DE", vm("one", "443"), vm("three", "8443"),
		"vless://u@a.example.com:443?encryption=none&type=grpc#DE grpc",
	}
	if got := dedupeLinks(in); !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestDedupeJSONConfigsComparesTheProxyOnly(t *testing.T) {
	mk := func(remark, addr string) json_util.RawMessage {
		return json_util.RawMessage(`{"remarks":"` + remark + `","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"address":"` + addr + `"}},{"tag":"direct"}]}`)
	}
	got := dedupeJSONConfigs([]json_util.RawMessage{mk("DE ru2", "ru.walk"), mk("DE ru4", "ru.walk"), mk("FI", "fi.walk"), json_util.RawMessage(`{"outbounds":[]}`)})
	if len(got) != 3 || !strings.Contains(string(got[0]), "DE ru2") || !strings.Contains(string(got[1]), "FI") {
		t.Fatalf("got %d configs: %s", len(got), got)
	}
}
