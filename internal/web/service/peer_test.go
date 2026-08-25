package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func initPeerTestDB(t *testing.T) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

// validPeer names the row after the calling test: under XUI_DB_TYPE=postgres
// every test shares one database, so a fixed name collides on the unique index.
func validPeer(t *testing.T) *model.Node {
	t.Helper()
	return &model.Node{
		Name: t.Name(), Scheme: "https", SubDomain: "sub2.example.com",
		SubPort: 2096, SubPath: "sub", Enable: true,
	}
}

func TestPeerNormalizeFillsDefaults(t *testing.T) {
	s := PeerService{}
	p := &model.Node{Name: "  eu-sub-2  ", SubDomain: "sub2.example.com", SubPort: 2096, SubPath: "sub", SubIps: []string{" 185.51.100.2 ", ""}}
	if err := s.normalize(p); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Name != "eu-sub-2" {
		t.Fatalf("name = %q, want trimmed", p.Name)
	}
	if p.Scheme != "https" {
		t.Fatalf("scheme = %q, want https", p.Scheme)
	}
	if p.SubPath != "/sub/" {
		t.Fatalf("subPath = %q, want /sub/", p.SubPath)
	}
	if p.BasePath != "/" {
		t.Fatalf("basePath = %q, want /", p.BasePath)
	}
	if len(p.SubIps) != 1 || p.SubIps[0] != "185.51.100.2" {
		t.Fatalf("ips = %v, want the single trimmed address", p.SubIps)
	}
}

func TestPeerNormalizeRejectsBadInput(t *testing.T) {
	s := PeerService{}
	tests := []struct {
		name   string
		mutate func(*model.Node)
	}{
		{"empty name", func(p *model.Node) { p.Name = "  " }},
		{"empty domain", func(p *model.Node) { p.SubDomain = "" }},
		{"domain with a scheme", func(p *model.Node) { p.SubDomain = "https://sub2.example.com" }},
		{"port out of range", func(p *model.Node) { p.SubPort = 70000 }},
		{"non-IP in ips", func(p *model.Node) { p.SubIps = []string{"not an ip"} }},
		{"hostname in ips", func(p *model.Node) { p.SubIps = []string{"sub2.example.com"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPeer(t)
			tt.mutate(p)
			if err := s.normalize(p); err == nil {
				t.Fatal("normalize accepted invalid input")
			}
		})
	}
}

// enable declares a DB default, which makes GORM drop a false value from the
// INSERT — a peer added as disabled would silently come back enabled.
func TestPeerCreateKeepsDisabledFlag(t *testing.T) {
	initPeerTestDB(t)
	s := PeerService{}
	p := validPeer(t)
	p.Enable = false
	if err := s.Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := s.GetById(p.Id)
	if err != nil {
		t.Fatalf("GetById: %v", err)
	}
	if stored.Enable {
		t.Fatal("peer created as disabled came back enabled")
	}
}

// Update writes the operator-owned columns only; probe results belong to the
// health job and must survive an edit.
func TestPeerUpdateKeepsObservedState(t *testing.T) {
	initPeerTestDB(t)
	s := PeerService{}
	p := validPeer(t)
	if err := s.Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.UpdateHealth(p.Id, PeerHealthPatch{
		Status: "online", LastHeartbeat: 1700000000, LatencyMs: 42, PublicKey: "deadbeef",
	}); err != nil {
		t.Fatalf("UpdateHealth: %v", err)
	}

	edit := validPeer(t)
	edit.SubDomain = "sub9.example.com"
	edit.SubIps = []string{"185.51.100.9"}
	if err := s.Update(p.Id, edit); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, err := s.GetById(p.Id)
	if err != nil {
		t.Fatalf("GetById: %v", err)
	}
	if stored.SubDomain != "sub9.example.com" {
		t.Fatalf("domain = %q, want the edited value", stored.SubDomain)
	}
	if len(stored.SubIps) != 1 || stored.SubIps[0] != "185.51.100.9" {
		t.Fatalf("ips = %v, want the edited list", stored.SubIps)
	}
	if stored.Status != "online" || stored.LatencyMs != 42 || stored.PublicKey != "deadbeef" {
		t.Fatalf("edit clobbered observed state: %+v", stored)
	}
}

func TestPeerLiveEndpointsFiltersAndOrders(t *testing.T) {
	initPeerTestDB(t)
	db := database.GetDB()
	// The assertion is over the whole live set, so start from an empty table:
	// under XUI_DB_TYPE=postgres every test shares one database.
	if err := db.Where("1 = 1").Delete(&model.Node{}).Error; err != nil {
		t.Fatalf("clear peers: %v", err)
	}
	seed := func(name, domain, status string, latency int, self bool, enable bool) {
		t.Helper()
		p := &model.Node{
			Name: name, Scheme: "https", SubDomain: domain, SubPort: 2096, SubPath: "/sub/",
			Role: model.NodeRoleMaster, Enable: enable, Status: status, LatencyMs: latency, IsSelf: self,
		}
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if !enable {
			if err := db.Model(model.Node{}).Where("id = ?", p.Id).Update("enable", false).Error; err != nil {
				t.Fatalf("disable %s: %v", name, err)
			}
		}
	}
	seed("slow", "slow.example.com", "online", 200, false, true)
	seed("fast", "fast.example.com", "online", 10, false, true)
	seed("down", "down.example.com", "offline", 5, false, true)
	seed("paused", "paused.example.com", "online", 5, false, false)
	seed("self", "self.example.com", "online", 1, true, true)

	s := PeerService{}
	withoutSelf, err := s.LiveEndpoints(false)
	if err != nil {
		t.Fatalf("LiveEndpoints(false): %v", err)
	}
	got := []string{}
	for _, e := range withoutSelf {
		got = append(got, e.Domain)
	}
	if len(got) != 2 || got[0] != "fast.example.com" || got[1] != "slow.example.com" {
		t.Fatalf("live endpoints = %v, want the two reachable peers fastest first", got)
	}

	withSelf, err := s.LiveEndpoints(true)
	if err != nil {
		t.Fatalf("LiveEndpoints(true): %v", err)
	}
	if len(withSelf) != 3 || withSelf[0].Domain != "self.example.com" {
		t.Fatalf("master list = %v, want this panel included and ordered by latency", withSelf)
	}
}
