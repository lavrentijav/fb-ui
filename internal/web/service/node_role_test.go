package service

import (
	"path/filepath"
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
