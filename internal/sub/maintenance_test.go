package sub

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A node taken out of service must stop being handed to clients, while the
// same client's inbounds elsewhere stay in the subscription.
func TestSubSkipsNodesInMaintenance(t *testing.T) {
	seedSubDB(t)
	db := database.GetDB()
	node := &model.Node{Name: "ru4", Address: "ru4.example.com", Port: 1367, Enable: true, Maintenance: true}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	onNode := seedSubInbound(t, "s1", "ru4-de", 2053, 1, wsTLSStream)
	if err := db.Model(onNode).Update("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	seedSubInbound(t, "s1", "local-fi", 2083, 2, wsTLSStream)

	links, _, _, _, err := NewSubService("").GetSubs("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(links, "\n")
	if strings.Contains(all, ":2053") || !strings.Contains(all, ":2083") {
		t.Fatalf("maintenance node still served or local one lost:\n%s", all)
	}
}
