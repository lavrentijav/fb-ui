package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The intermediate build stored sibling panels in their own master_peers table.
// Folding them into nodes must keep every row and the observed state on it,
// because a lost master silently stops being advertised to clients.
func TestMigrateMasterPeersIntoNodes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	if err := db.Exec(`CREATE TABLE master_peers (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, remark TEXT, scheme TEXT,
		domain TEXT, port INTEGER, sub_path TEXT, base_path TEXT, ips TEXT,
		enable NUMERIC, allow_private_address NUMERIC, is_self NUMERIC,
		public_key TEXT, status TEXT, last_heartbeat INTEGER, latency_ms INTEGER,
		last_error TEXT, created_at INTEGER, updated_at INTEGER)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO master_peers
		(name, remark, scheme, domain, port, sub_path, base_path, ips, enable,
		 allow_private_address, is_self, public_key, status, last_heartbeat, latency_ms, last_error)
		VALUES
		('eu-sub-2','sibling','https','sub2.example.com',2096,'/sub/','/','["185.51.100.2"]',1,0,0,'deadbeef','online',1700000000,42,''),
		('paused','', 'https','sub3.example.com',2096,'/sub/','/','[]',0,0,0,'','offline',0,0,'unreachable')`).Error; err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	if err := migrateMasterPeersIntoNodes(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if db.Migrator().HasTable("master_peers") {
		t.Fatal("master_peers still exists after the migration")
	}

	var moved []*model.Node
	if err := db.Where("role = ?", model.NodeRoleMaster).Order("id asc").Find(&moved).Error; err != nil {
		t.Fatalf("load migrated masters: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("migrated %d masters, want 2", len(moved))
	}

	live := moved[0]
	if live.Name != "eu-sub-2" || live.SubDomain != "sub2.example.com" || live.SubPort != 2096 {
		t.Fatalf("addressing lost: %+v", live)
	}
	if len(live.SubIps) != 1 || live.SubIps[0] != "185.51.100.2" {
		t.Fatalf("static IPs lost: %v", live.SubIps)
	}
	if live.PublicKey != "deadbeef" || live.Status != "online" || live.LatencyMs != 42 {
		t.Fatalf("observed state lost: %+v", live)
	}
	if live.Address != "sub2.example.com" {
		t.Fatalf("address = %q, want the subscription domain", live.Address)
	}

	// A disabled peer must not come back enabled: the column declares a default,
	// which is exactly the trap that makes GORM drop a false value.
	if moved[1].Enable {
		t.Fatal("a disabled master was migrated as enabled")
	}

	// Running it again on a database that no longer has the table is a no-op.
	if err := migrateMasterPeersIntoNodes(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
