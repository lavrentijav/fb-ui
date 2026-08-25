package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func initNodeRoleTestDB(t *testing.T) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

// Masters and controlled nodes share one table, so every dispatch path has to
// filter by role: pushing an inbound to a sibling master would be wrong, and
// probing a master's panel API is not how a master is checked.
func TestNodeAndPeerListsAreDisjointByRole(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}

	controlled := &model.Node{
		Name: "de-fra-1", Scheme: "https", Address: "node1.example.com", Port: 2053,
		BasePath: "/", Role: model.NodeRoleNode, Enable: true,
	}
	if err := db.Create(controlled).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	master := &model.Node{
		Name: "eu-sub-2", Scheme: "https", Address: "sub2.example.com",
		SubDomain: "sub2.example.com", SubPort: 2096, SubPath: "/sub/",
		Role: model.NodeRoleMaster, Enable: true,
	}
	if err := db.Create(master).Error; err != nil {
		t.Fatalf("seed master: %v", err)
	}

	nodes, err := (&NodeService{}).GetAll()
	if err != nil {
		t.Fatalf("NodeService.GetAll: %v", err)
	}
	for _, n := range nodes {
		if n.Role == model.NodeRoleMaster {
			t.Fatalf("master %q reached the node dispatch list", n.Name)
		}
	}
	if len(nodes) != 1 || nodes[0].Name != "de-fra-1" {
		t.Fatalf("controlled nodes = %v, want only de-fra-1", nodeNames(nodes))
	}

	peers, err := (&PeerService{}).GetAll()
	if err != nil {
		t.Fatalf("PeerService.GetAll: %v", err)
	}
	if len(peers) != 1 || peers[0].Name != "eu-sub-2" {
		t.Fatalf("masters = %v, want only eu-sub-2", nodeNames(peers))
	}
}

// A row created through the peer API is a master whatever the caller sent, so a
// mis-typed payload cannot smuggle a sibling panel into the dispatch path.
func TestPeerCreateForcesMasterRole(t *testing.T) {
	initNodeRoleTestDB(t)
	s := PeerService{}
	p := &model.Node{
		Name: "role-forced", Scheme: "https", SubDomain: "sub9.example.com",
		SubPort: 2096, SubPath: "/sub/", Role: model.NodeRoleNode, Enable: true,
	}
	if err := s.Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := s.GetById(p.Id)
	if err != nil {
		t.Fatalf("GetById: %v", err)
	}
	if stored.Role != model.NodeRoleMaster {
		t.Fatalf("role = %q, want %q", stored.Role, model.NodeRoleMaster)
	}
	if stored.Address != "sub9.example.com" {
		t.Fatalf("address = %q, want it kept in sync with the subscription domain", stored.Address)
	}
}

func nodeNames(nodes []*model.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

// A role flip is an edit of one row, and the health the old role observed must
// not carry over: a node last seen "online" would otherwise be advertised to
// clients as a live fallback master without ever being probed as one.
func TestSetRoleNodeToMasterResetsObservedHealth(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	node := &model.Node{
		Name: "flip-1", Scheme: "https", Address: "node1.example.com", Port: 2053,
		BasePath: "/", Role: model.NodeRoleNode, Enable: true, ApiToken: "secret-token",
		Status: "online", LatencyMs: 42, LastHeartbeat: 1700000000, Guid: "guid-1",
		XrayVersion: "25.10.31", XrayState: "running",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	s := NodeService{}
	if err := s.SetRole(node.Id, &NodeRoleChangeRequest{
		Role: model.NodeRoleMaster, SubDomain: "sub9.example.com", SubPort: 2096, SubPath: "/sub/",
	}); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	master, err := (&PeerService{}).GetById(node.Id)
	if err != nil {
		t.Fatalf("GetById after the flip: %v", err)
	}
	if master.Status != "unknown" {
		t.Fatalf("status = %q, want it reset to unknown", master.Status)
	}
	if master.LatencyMs != 0 || master.LastHeartbeat != 0 || master.XrayState != "" {
		t.Fatalf("stale node health survived: latency=%d heartbeat=%d xrayState=%q",
			master.LatencyMs, master.LastHeartbeat, master.XrayState)
	}
	if master.ApiToken != "" {
		t.Fatal("the node credential survived a flip to master")
	}
	if master.Address != "sub9.example.com" {
		t.Fatalf("address = %q, want it synced to the subscription domain", master.Address)
	}

	nodes, err := s.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("controlled nodes = %v, want the flipped row gone", nodeNames(nodes))
	}
}

// Inbounds keep a node_id the node jobs no longer visit once the row is a
// master, so they would stop being synced anywhere without ever erroring.
func TestSetRoleRefusesWhileInboundsAreAttached(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	node := &model.Node{
		Name: "busy-1", Scheme: "https", Address: "node2.example.com", Port: 2053,
		BasePath: "/", Role: model.NodeRoleNode, Enable: true, ApiToken: "t",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := db.Create(&model.Inbound{
		Tag: "busy-in", Enable: true, Port: 443, Protocol: model.VLESS,
		Settings: `{"clients":[]}`, NodeID: &node.Id,
	}).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}

	err := (&NodeService{}).SetRole(node.Id, &NodeRoleChangeRequest{
		Role: model.NodeRoleMaster, SubDomain: "sub8.example.com", SubPort: 2096,
	})
	if err == nil {
		t.Fatal("SetRole turned a node with inbounds into a master")
	}
	if !strings.Contains(err.Error(), "1 inbound(s)") {
		t.Fatalf("error = %q, want it to name the attached inbounds", err.Error())
	}
	stored := &model.Node{}
	if err := db.Where("id = ?", node.Id).First(stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Role != model.NodeRoleNode {
		t.Fatalf("role = %q, want the refused flip to change nothing", stored.Role)
	}
}

func TestSetRoleMasterToNode(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	master := &model.Node{
		Name: "sib-1", Scheme: "https", Address: "sub2.example.com", SubDomain: "sub2.example.com",
		SubPort: 2096, SubPath: "/sub/", Role: model.NodeRoleMaster, Enable: true,
		Status: "online", LatencyMs: 17, PublicKey: "3d40",
	}
	if err := db.Create(master).Error; err != nil {
		t.Fatalf("seed master: %v", err)
	}
	s := NodeService{}

	// Without a credential the panel could not reach the node API it just
	// promised to manage, so the flip has to be refused rather than half-done.
	err := s.SetRole(master.Id, &NodeRoleChangeRequest{Role: model.NodeRoleNode, Port: 2053})
	if err == nil || !strings.Contains(err.Error(), "apiToken is required") {
		t.Fatalf("error = %v, want the missing credential to be refused", err)
	}

	if err := s.SetRole(master.Id, &NodeRoleChangeRequest{
		Role: model.NodeRoleNode, Address: "node9.example.com", Port: 2053, ApiToken: "t0ken",
	}); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	stored, err := s.GetById(master.Id)
	if err != nil {
		t.Fatalf("GetById: %v", err)
	}
	if stored.Role != model.NodeRoleNode || stored.Address != "node9.example.com" || stored.Port != 2053 {
		t.Fatalf("stored = %+v, want a node at node9.example.com:2053", stored)
	}
	if stored.PublicKey != "" || stored.Status != "unknown" {
		t.Fatalf("master identity survived: publicKey=%q status=%q", stored.PublicKey, stored.Status)
	}
	if !stored.ConfigDirty {
		t.Fatal("the new node was not queued for its first config push")
	}
	// Its subscription addressing is kept so flipping back does not re-ask it.
	if stored.SubDomain != "sub2.example.com" {
		t.Fatalf("subDomain = %q, want it preserved for a flip back", stored.SubDomain)
	}
}

// The row flagged as this very panel must never become a node of itself.
func TestSetRoleRefusesSelfMaster(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	self := &model.Node{
		Name: "this-panel", Scheme: "https", Address: "sub1.example.com", SubDomain: "sub1.example.com",
		SubPort: 2096, SubPath: "/sub/", Role: model.NodeRoleMaster, Enable: true, IsSelf: true,
	}
	if err := db.Create(self).Error; err != nil {
		t.Fatalf("seed self master: %v", err)
	}
	err := (&NodeService{}).SetRole(self.Id, &NodeRoleChangeRequest{
		Role: model.NodeRoleNode, Address: "sub1.example.com", Port: 2053, ApiToken: "t",
	})
	if err == nil || !strings.Contains(err.Error(), "this panel itself") {
		t.Fatalf("error = %v, want the self row to be refused", err)
	}
}

// A round trip must be lossless: the master fields the node role never touches
// have to survive it, or flipping back quietly drops the static IPs clients
// were being handed.
func TestSetRoleRoundTripKeepsTheOtherRolesFields(t *testing.T) {
	initNodeRoleTestDB(t)
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear nodes: %v", err)
	}
	master := &model.Node{
		Name: "sib-2", Scheme: "https", Address: "sub3.example.com", SubDomain: "sub3.example.com",
		SubPort: 2096, SubPath: "/sub/", SubIps: []string{"185.51.100.3"}, Role: model.NodeRoleMaster,
		Enable: true, AllowPrivateAddress: true,
	}
	if err := db.Create(master).Error; err != nil {
		t.Fatalf("seed master: %v", err)
	}
	s := NodeService{}

	if err := s.SetRole(master.Id, &NodeRoleChangeRequest{
		Role: model.NodeRoleNode, Address: "node3.example.com", Port: 2053, ApiToken: "t0ken",
	}); err != nil {
		t.Fatalf("SetRole to node: %v", err)
	}
	if err := s.SetRole(master.Id, &NodeRoleChangeRequest{Role: model.NodeRoleMaster}); err != nil {
		t.Fatalf("SetRole back to master: %v", err)
	}

	stored, err := (&PeerService{}).GetById(master.Id)
	if err != nil {
		t.Fatalf("GetById: %v", err)
	}
	if len(stored.SubIps) != 1 || stored.SubIps[0] != "185.51.100.3" {
		t.Fatalf("subIps = %v, want them to survive the round trip", stored.SubIps)
	}
	if !stored.AllowPrivateAddress {
		t.Fatal("allowPrivateAddress was cleared by a request that never mentioned it")
	}
	if stored.SubDomain != "sub3.example.com" || stored.SubPort != 2096 {
		t.Fatalf("subscription endpoint = %s:%d, want it unchanged", stored.SubDomain, stored.SubPort)
	}
}
