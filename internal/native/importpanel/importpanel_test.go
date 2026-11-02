package importpanel_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mhsanaei/3x-ui/v3/internal/native/importpanel"
)

// fixture builds a tiny old-panel database with the columns the importer reads.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER,
		 share_addr_strategy TEXT, share_addr TEXT)`,
		`CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, uuid TEXT, total_gb INTEGER,
		 expiry_time INTEGER, limit_ip INTEGER, enable INTEGER, group_name TEXT, sub_id TEXT)`,
		`CREATE TABLE client_inbounds (client_id INTEGER, inbound_id INTEGER)`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE client_groups (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE settings (id INTEGER PRIMARY KEY, key TEXT)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO nodes(id,name) VALUES(2,'ru4')`, nil},
		{`INSERT INTO inbounds VALUES(1,'in-443-tcp','RU entry',443,'vless','{}',
			'{"network":"ws","wsSettings":{"path":"/ws","headers":{"Host":"ru.example"}}}',2,1,'node','')`, nil},
		{`INSERT INTO inbounds VALUES(2,'xhttp-one','DE xhttp',8443,'vless','{}',
			'{"network":"xhttp","xhttpSettings":{"path":"/xh","host":"de.example"}}',2,1,'custom','cdn.example')`, nil},
		{`INSERT INTO inbounds VALUES(3,'trojan-one','Trojan',443,'trojan','{}','{}',2,1,'node','')`, nil},
		{`INSERT INTO inbounds VALUES(4,'local-one','Local',2053,'vless','{}','{"network":"ws"}',NULL,1,'node','')`, nil},
		{`INSERT INTO inbounds VALUES(5,'grpc-one','gRPC',443,'vless','{}','{"network":"grpc"}',2,1,'node','')`, nil},
		{`INSERT INTO clients VALUES(1,'alice','uuid-alice',10737418240,1798761600000,2,1,'','sub1')`, nil},
		{`INSERT INTO clients VALUES(2,'bob','uuid-bob',0,0,0,0,'','sub2')`, nil},
		{`INSERT INTO clients VALUES(3,'orphan','uuid-orphan',0,0,0,1,'','sub3')`, nil},
		{`INSERT INTO client_inbounds VALUES(1,1)`, nil},
		{`INSERT INTO client_inbounds VALUES(1,2)`, nil},
		{`INSERT INTO client_inbounds VALUES(2,1)`, nil},
		{`INSERT INTO client_groups(id,name) VALUES(1,'friends')`, nil},
		{`INSERT INTO settings(id,key) VALUES(1,'webPort')`, nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(r.q, r.args...); err != nil {
			t.Fatalf("%s: %v", r.q, err)
		}
	}
	return path
}

func TestReadAndPlanMapTheOldPanel(t *testing.T) {
	snap, err := importpanel.Read(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Inbounds) != 5 || len(snap.Clients) != 3 || len(snap.Links) != 3 {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.Nodes[2] != "ru4" {
		t.Fatalf("nodes %v", snap.Nodes)
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1"})

	// ws on node 2 with the ShareAddrStrategy=node host header, xhttp with a
	// custom CDN address, and the old panel's own inbound on the local node
	if len(rep.Inbounds) != 3 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	byTag := map[string]importpanel.PlannedInbound{}
	for _, in := range rep.Inbounds {
		byTag[in.Tag] = in
	}
	ws, ok := byTag["in-443-tcp"]
	if !ok || ws.Node != "ru4" || ws.Port != 443 || ws.Network != "ws" || ws.Path != "/ws" || ws.Host != "ru.example" {
		t.Fatalf("ws %+v", ws)
	}
	xh := byTag["xhttp-one"]
	if xh.Node != "ru4" || xh.Network != "xhttp" || xh.Path != "/xh" || xh.Host != "cdn.example" {
		t.Fatalf("xhttp %+v", xh)
	}
	local := byTag["local-one"]
	if local.Node != "fi1" || local.Network != "ws" || local.Path != "/" {
		t.Fatalf("local %+v", local)
	}

	// six client records: alice on two inbounds, bob on one, and the orphan
	// has no link; bob is disabled in the old panel
	if len(rep.Clients) != 3 {
		t.Fatalf("clients %+v", rep.Clients)
	}
	for _, c := range rep.Clients {
		switch c.Email {
		case "alice":
			if c.UUID != "uuid-alice" || c.TotalBytes != 10737418240 || c.ExpiresAt != 1798761600000 || c.LimitIP != 2 || !c.Enable {
				t.Fatalf("alice %+v", c)
			}
		case "bob":
			if c.Enable {
				t.Fatalf("bob should stay disabled: %+v", c)
			}
		default:
			t.Fatalf("unexpected client %+v", c)
		}
	}
	// the trojan and grpc inbounds are reported, not silently dropped
	var trojan, grpc bool
	for _, sk := range rep.Skipped {
		if sk.Entity == `inbound "trojan-one" (trojan)` {
			trojan = true
		}
		if sk.Entity == `inbound "grpc-one"` && sk.Reason == `transport "grpc" has no native equivalent yet` {
			grpc = true
		}
	}
	if !trojan || !grpc {
		t.Fatalf("skips %+v", rep.Skipped)
	}
	found := false
	for _, n := range rep.Notes {
		if n == "1 client(s) have no client_inbounds row; if the panel predates that table they live in each inbound's settings JSON and must be linked by hand" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes %v", rep.Notes)
	}
	if len(rep.Groups) != 1 || rep.Groups[0] != "friends" {
		t.Fatalf("groups %v", rep.Groups)
	}
}

func TestPlanWithoutALocalNodeReportsThePanelsOwnInbounds(t *testing.T) {
	snap, err := importpanel.Read(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{})
	for _, in := range rep.Inbounds {
		if in.Node == "" {
			t.Fatalf("an inbound without a node: %+v", in)
		}
	}
	var skip bool
	for _, sk := range rep.Skipped {
		if sk.Entity == `inbound "local-one"` {
			skip = true
		}
	}
	if !skip {
		t.Fatalf("the local inbound was not reported: %+v", rep.Skipped)
	}
}
