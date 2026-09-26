package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The first build tied a certificate to one node through certificates.node_id.
// Its binding and push state must survive as a delivery row, or the node stops
// receiving renewals without anyone noticing.
func TestMigrateCertificateNodeIntoDeliveries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	for _, col := range []string{"node_id INTEGER DEFAULT 0", "pushed_at INTEGER", "push_error TEXT"} {
		if err := db.Exec("ALTER TABLE certificates ADD COLUMN " + col).Error; err != nil {
			t.Fatalf("add legacy column: %v", err)
		}
	}
	if err := db.Exec(`INSERT INTO certificates (id, domains, challenge, cert_file, key_file, status, node_id, pushed_at, push_error) VALUES
		(1, '["de2.example.com"]', 'dns01', '/c/de2', '/k/de2', 'valid', 1, 1700000000, ''),
		(2, '["nl2.example.com"]', 'dns01', '/c/nl2', '/k/nl2', 'valid', 2, 0, 'node offline'),
		(3, '["panel.example.com"]', 'dns01', '/c/p', '/k/p', 'valid', 0, NULL, NULL)`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	for range 2 {
		if err := migrateCertificateNodeIntoDeliveries(); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	var got []model.CertificateDelivery
	db.Order("certificate_id asc").Find(&got)
	if len(got) != 2 ||
		got[0].CertificateId != 1 || got[0].NodeId != 1 || got[0].PushedAt != 1700000000 ||
		got[1].CertificateId != 2 || got[1].NodeId != 2 || got[1].PushedAt != 0 || got[1].PushError != "node offline" {
		t.Fatalf("deliveries = %+v", got)
	}
	var left int64
	db.Table("certificates").Where("node_id > 0").Count(&left)
	if left != 0 {
		t.Fatalf("%d certificates still carry node_id; a rerun would duplicate them", left)
	}
}
