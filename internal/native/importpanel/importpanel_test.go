package importpanel_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mhsanaei/3x-ui/v3/internal/native/importpanel"
)

const (
	aliceUUID = "11111111-1111-1111-1111-111111111111"
	bobUUID   = "22222222-2222-2222-2222-222222222222"
	carolUUID = "33333333-3333-3333-3333-333333333333"
	daveUUID  = "44444444-4444-4444-4444-444444444444"
	frankUUID = "55555555-5555-5555-5555-555555555555"
)

// fixture builds a tiny old-panel database in this fork's shape: a clients
// table, client_inbounds and groups.
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
		{`INSERT INTO clients VALUES(1,'alice','` + aliceUUID + `',10737418240,1798761600000,2,1,'friends','sub1')`, nil},
		{`INSERT INTO clients VALUES(2,'bob','` + bobUUID + `',0,0,0,0,'friends','sub2')`, nil},
		{`INSERT INTO clients VALUES(3,'orphan','` + carolUUID + `',0,0,0,1,'','sub3')`, nil},
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

// upstreamFixture is the upstream 3x-ui shape: clients live inside each
// inbound's settings JSON and there is no clients/client_inbounds table.
func upstreamFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'up-ws','Upstream ws',443,'vless',
		 '{"clients":[{"id":"` + aliceUUID + `","email":"alice","enable":true,"totalGB":1073741824,"expiryTime":0,"limitIp":1,"subId":"sa"},
		              {"id":"` + bobUUID + `","email":"bob","enable":false,"totalGB":0,"expiryTime":0,"limitIp":0,"subId":"sb"}]}',
		 '{"network":"ws","wsSettings":{"path":"/up"}}',1)`,
		`INSERT INTO inbounds VALUES(2,'up-trojan','Upstream trojan',8443,'trojan',
		 '{"clients":[{"password":"pw","email":"carol","enable":true}]}','{}',1)`,
		`INSERT INTO inbounds VALUES(3,'up-xhttp','Upstream xhttp',2053,'vless',
		 '{"clients":[{"id":"` + daveUUID + `","email":"dave","enable":true,"totalGB":0,"expiryTime":0,"limitIp":0,"subId":"sd"}]}',
		 '{"network":"xhttp","xhttpSettings":{"path":"/xh","host":"cdn.example"}}',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
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
	if snap.Schema.Style != "client-records" || snap.Schema.Clients != 3 || snap.Schema.SettingsClients != 0 {
		t.Fatalf("schema %+v", snap.Schema)
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

	// alice on two inbounds, bob on one; the orphan has no link and bob is
	// disabled in the old panel
	if len(rep.Clients) != 3 {
		t.Fatalf("clients %+v", rep.Clients)
	}
	for _, c := range rep.Clients {
		switch c.Email {
		case "alice":
			if c.UUID != aliceUUID || c.TotalBytes != 10737418240 || c.ExpiresAt != 1798761600000 || c.LimitIP != 2 || !c.Enable {
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
	if rep.Aliases["alice"] != "sub1" {
		t.Fatalf("aliases %v", rep.Aliases)
	}

	// the trojan and grpc inbounds are reported, not silently dropped, and a
	// grpc inbound's clients stay behind with it
	var trojan, grpc bool
	for _, sk := range rep.Skipped {
		if sk.Entity == `inbound "trojan-one" (trojan)` {
			trojan = true
		}
		if sk.Entity == `inbound "grpc-one"` && strings.HasPrefix(sk.Reason, `transport "grpc" has no native equivalent yet`) {
			grpc = true
		}
	}
	if !trojan || !grpc {
		t.Fatalf("skips %+v", rep.Skipped)
	}
	found := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "1 client(s) have no inbound link") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes %v", rep.Notes)
	}
	if len(rep.Groups) != 1 || rep.Groups[0] != "friends" {
		t.Fatalf("groups %v", rep.Groups)
	}
	if got := rep.GroupMembers["friends"]; len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("group members %v", got)
	}
}

func TestUpstreamSettingsShapeIsReadAndReported(t *testing.T) {
	snap, err := importpanel.Read(upstreamFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Schema.Style != "upstream-settings" || snap.Schema.SettingsClients != 4 {
		t.Fatalf("schema %+v", snap.Schema)
	}
	if len(snap.Clients) != 4 || len(snap.Links) != 4 {
		t.Fatalf("snapshot clients=%d links=%d", len(snap.Clients), len(snap.Links))
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1"})
	if len(rep.Inbounds) != 2 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	if len(rep.Clients) != 3 {
		t.Fatalf("clients %+v", rep.Clients)
	}
	for _, c := range rep.Clients {
		if c.Email == "carol" {
			t.Fatalf("a trojan client was planned: %+v", c)
		}
		if c.Email == "bob" && c.Enable {
			t.Fatalf("bob's absent/enabled state was not honoured: %+v", c)
		}
		if c.Email == "alice" && (c.SubID != "sa" || c.UUID != aliceUUID) {
			t.Fatalf("alice %+v", c)
		}
	}
	var trojan bool
	for _, sk := range rep.Skipped {
		if sk.Entity == `inbound "up-trojan" (trojan)` && strings.Contains(sk.Reason, "1 client(s) stay in the old panel") {
			trojan = true
		}
	}
	if !trojan {
		t.Fatalf("skips %+v", rep.Skipped)
	}
	var note bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "inbounds.settings") {
			note = true
		}
	}
	if !note {
		t.Fatalf("notes %v", rep.Notes)
	}
}

func TestReadToleratesMissingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, port INTEGER, protocol TEXT,
		 settings TEXT, stream_settings TEXT, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'ss','8388','shadowsocks','{"clients":[{"email":"x","password":"p"}]}','{}',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	snap, err := importpanel.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Schema.Style != "upstream-settings" || snap.Schema.Clients != 0 {
		t.Fatalf("schema %+v", snap.Schema)
	}
	rep := importpanel.Plan(snap, importpanel.Options{})
	if len(rep.Inbounds) != 0 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	var skip bool
	for _, sk := range rep.Skipped {
		if strings.Contains(sk.Entity, "shadowsocks") && strings.Contains(sk.Reason, "1 client(s) stay") {
			skip = true
		}
	}
	if !skip {
		t.Fatalf("the unsupported inbound was not reported: %+v", rep.Skipped)
	}
}

func TestReadDoesNotChangeTheSourceDatabase(t *testing.T) {
	path := fixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore, _ := os.ReadDir(filepath.Dir(path))
	if _, err := importpanel.Read(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatalf("the source database changed: %s -> %s",
			hex.EncodeToString(mustSum(before)), hex.EncodeToString(mustSum(after)))
	}
	entriesAfter, _ := os.ReadDir(filepath.Dir(path))
	if len(entriesAfter) != len(entriesBefore) {
		// mode=ro must not leave a -wal or -shm behind
		t.Fatalf("the read created files: before %d, after %d", len(entriesBefore), len(entriesAfter))
	}
}

func mustSum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func TestRedactedReportHidesIdentifiers(t *testing.T) {
	snap, err := importpanel.Read(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1"}).Redacted()
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{aliceUUID, bobUUID, "sub1", "sub2"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s leaked into the redacted report: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "1111...1111") {
		t.Fatalf("the masked UUID is not recognisable: %s", raw)
	}
	md := rep.Markdown()
	if strings.Contains(md, aliceUUID) || !strings.Contains(md, "## Inbounds") {
		t.Fatalf("markdown %s", md)
	}
}

func TestGroupFromClientRowsAndClientGroups(t *testing.T) {
	snap, err := importpanel.Read(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.GroupMembers["friends"]) != 2 || snap.GroupMembers["friends"][0] != "alice" {
		t.Fatalf("members %v", snap.GroupMembers)
	}
}

func TestPlanUsesTheDefaultPublicHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'no-host','no host',443,'vless','{}','{"network":"ws"}',0,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	snap, err := importpanel.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1", DefaultHost: "cdn.example"})
	if len(rep.Inbounds) != 1 || rep.Inbounds[0].Host != "cdn.example" {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
}

func TestPlanUsesAPerNodePublicHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO nodes VALUES(1,'de2'),(2,'fi1')`,
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'de-in','de',443,'vless','{}','{"network":"ws"}',1,1)`,
		`INSERT INTO inbounds VALUES(2,'fi-in','fi',8443,'vless','{}','{"network":"ws"}',2,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	snap, err := importpanel.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{
		DefaultHost: "cdn.example",
		Hosts:       map[string]string{"de2": "de2.firewalk.top", "fi1": "fi1.firewalk.top"},
	})
	if len(rep.Inbounds) != 2 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	want := map[string]string{"de2": "de2.firewalk.top", "fi1": "fi1.firewalk.top"}
	for _, in := range rep.Inbounds {
		if in.Host != want[in.Node] {
			t.Fatalf("%s host = %q, want %q", in.Node, in.Host, want[in.Node])
		}
	}
}

func TestPlanRefusesDuplicateTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dup.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'same','one',443,'vless','{}','{"network":"ws"}',0,1)`,
		`INSERT INTO inbounds VALUES(2,'same','two',8443,'vless','{}','{"network":"ws"}',0,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	snap, err := importpanel.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1"})
	if len(rep.Inbounds) != 1 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	var skip bool
	for _, sk := range rep.Skipped {
		if strings.Contains(sk.Reason, "already used") {
			skip = true
		}
	}
	if !skip {
		t.Fatalf("a duplicate tag was not reported: %+v", rep.Skipped)
	}
}

// The old panel's "nfc_" remark marks an inbound the bot must not provision
// onto; the plan carries that as Reserved so apply can set the inbound param.
func TestPlanMarksNfcRemarkReserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nfc.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER)`,
		`INSERT INTO inbounds VALUES(1,'n1-in-12443-tcp','nfc_de2',12443,'vless','{}','{"network":"ws"}',0,1)`,
		`INSERT INTO inbounds VALUES(2,'n2-in-12444-tcp','DE public',12444,'vless','{}','{"network":"ws"}',0,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	snap, err := importpanel.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := importpanel.Plan(snap, importpanel.Options{LocalNode: "fi1"})
	if len(rep.Inbounds) != 2 {
		t.Fatalf("inbounds %+v", rep.Inbounds)
	}
	byTag := map[string]bool{}
	for _, in := range rep.Inbounds {
		byTag[in.Tag] = in.Reserved
	}
	if !byTag["n1-in-12443-tcp"] {
		t.Fatalf("nfc_ inbound not reserved: %+v", rep.Inbounds)
	}
	if byTag["n2-in-12444-tcp"] {
		t.Fatalf("normal inbound reserved: %+v", rep.Inbounds)
	}
}
