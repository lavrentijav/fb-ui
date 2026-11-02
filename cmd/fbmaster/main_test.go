package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/importpanel"
	"github.com/mhsanaei/3x-ui/v3/internal/native/masterjoin"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func writeConfig(t *testing.T, dir string, extra map[string]any) string {
	t.Helper()
	cfg := map[string]any{
		"name":     "fi1",
		"masters":  []string{"fi1"},
		"storeDir": filepath.Join(dir, "js"),
		"dataDir":  filepath.Join(dir, "data"),
	}
	for k, v := range extra {
		cfg[k] = v
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(io.Discard, "", 0)
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.EdgeListen != ":443" || c.ClientPort != 4222 || c.GatewayPort != 7222 {
		t.Fatalf("defaults %+v", c)
	}
	if c.PublicName != "fi1" {
		t.Fatalf("public name %q", c.PublicName)
	}
	if c.CACert != filepath.Join(dir, "data", "ca.pem") || c.Cert != filepath.Join(dir, "data", "master.pem") {
		t.Fatalf("identity paths %q %q", c.CACert, c.Cert)
	}
	if err := checkConfig(c); err != nil {
		t.Fatalf("check: %v", err)
	}

	// a peer that names this master, or a non-nats URL, is refused
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"peers": map[string]string{"fi1": "nats://x:7222"}})); err == nil {
		t.Fatal("a peer naming this master was accepted")
	}
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"peers": map[string]string{"ru2": "http://x:7222"}})); err == nil {
		t.Fatal("a non-nats peer URL was accepted")
	}
	// a missing name is refused
	if _, err := load(writeConfig(t, t.TempDir(), map[string]any{"name": ""})); err == nil {
		t.Fatal("an empty name was accepted")
	}
}

func TestCheckRefusesAPublicAdminListener(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, map[string]any{"adminListen": "0.0.0.0:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(c); err == nil {
		t.Fatal("a public admin listener passed the check")
	}
	c.AdminAllowPublic = true
	if err := checkConfig(c); err != nil {
		t.Fatalf("explicit public listener: %v", err)
	}
}

func TestFirstRunCreatesTheCAAndIdentity(t *testing.T) {
	dir := t.TempDir()
	c, err := load(writeConfig(t, dir, nil))
	if err != nil {
		t.Fatal(err)
	}
	logger := testLogger(t)
	// without -create-ca the master refuses to mint a CA
	if _, err := ensureCA(c, false, logger); err == nil {
		t.Fatal("a missing CA was accepted without -create-ca")
	}
	ca, err := ensureCA(c, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.CACert); err != nil {
		t.Fatalf("CA cert not written: %v", err)
	}
	if st, err := os.Stat(c.CAKey); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("CA key permissions: %v %v", st, err)
	}
	// a second run loads the same CA instead of minting another
	again, err := ensureCA(c, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	if again.Cert.SerialNumber.Cmp(ca.Cert.SerialNumber) != 0 {
		t.Fatal("the CA changed on the second run")
	}
	// and this master gets a certificate signed by it, written 0600
	id, err := ensureIdentity(c, ca, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Certificate) == 0 {
		t.Fatal("no certificate")
	}
	if st, err := os.Stat(c.Key); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("master key permissions: %v %v", st, err)
	}
	if _, err := ensureIdentity(c, ca, logger); err != nil {
		t.Fatalf("a second run did not reuse the identity: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The master starts from a config file alone: a fresh CA, its own
// certificate, the admin surface on loopback with the panel credential, and
// /healthz open.
func TestMasterStartsAndGuardsItsAdminSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a bus and an edge")
	}
	dir := t.TempDir()
	client, gateway, api := freePort(t), freePort(t), freePort(t)
	admin, edge := freePort(t), freePort(t)
	c, err := load(writeConfig(t, dir, map[string]any{
		"clientPort": client, "gatewayHost": "127.0.0.1", "gatewayPort": gateway,
		"apiListen":   "127.0.0.1:" + strconv.Itoa(api),
		"adminListen": "127.0.0.1:" + strconv.Itoa(admin),
		"edgeListen":  "127.0.0.1:" + strconv.Itoa(edge),
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- serve(ctx, c, true, testLogger(t)) }()

	base := "http://127.0.0.1:" + strconv.Itoa(admin)
	deadline := time.Now().Add(30 * time.Second)
	var health int
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			health = resp.StatusCode
			resp.Body.Close()
			if health == http.StatusOK {
				break
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("the master exited: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if health != http.StatusOK {
		t.Fatalf("healthz never became ready (last %d)", health)
	}
	// without a credential the admin surface is closed
	resp, err := http.Get(base + "/templates")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated admin call answered %d", resp.StatusCode)
	}
	// the CA and identity were created on first run
	for _, p := range []string{c.CACert, c.Cert} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(15 * time.Second):
		t.Fatal("the master did not stop")
	}
}

// A new master joins with a token minted by a live one: it gets the CA and a
// certificate, and the token works once.
func TestJoinWritesTheIdentityFromAToken(t *testing.T) {
	liveCA, err := fleetca.New("fleet")
	if err != nil {
		t.Fatal(err)
	}
	h := modtest.NewHost("fi1")
	secret, err := masterjoin.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := masterjoin.Mint(context.Background(), writer{h}, secret, "ru2", time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(masterjoin.Handler(liveCA, writer{h}, h, time.Now))
	defer srv.Close()

	dir := t.TempDir()
	token := masterjoin.Token(secret, liveCA)
	if err := runJoin([]string{"-master", srv.URL, "-token", token, "-name", "ru2", "-dir", dir, "-hosts", "ru2.m.example"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ca.pem", "master.pem", "master-key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	// the token is spent
	if err := runJoin([]string{"-master", srv.URL, "-token", token, "-name", "ru2", "-dir", dir}, io.Discard); err == nil {
		t.Fatal("the token was accepted twice")
	}
}

// writer adapts the test host to masterjoin.Writer.
type writer struct{ h *modtest.Host }

func (w writer) Set(ctx context.Context, table, key string, fields map[string]any) error {
	_, err := w.h.Set(ctx, table, key, fields)
	return err
}
func (w writer) Delete(ctx context.Context, table, key string) error {
	_, err := w.h.Delete(ctx, table, key)
	return err
}

// importFixture builds a minimal old-panel database.
func importFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
		 protocol TEXT, settings TEXT, stream_settings TEXT, node_id INTEGER, enable INTEGER,
		 share_addr_strategy TEXT, share_addr TEXT)`,
		`CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, uuid TEXT, total_gb INTEGER,
		 expiry_time INTEGER, limit_ip INTEGER, enable INTEGER, group_name TEXT, sub_id TEXT)`,
		`CREATE TABLE client_inbounds (client_id INTEGER, inbound_id INTEGER)`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE client_groups (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO nodes VALUES(2,'ru4')`,
		`INSERT INTO inbounds VALUES(1,'in-443','RU',443,'vless','{}',
		 '{"network":"ws","wsSettings":{"path":"/ws","headers":{"Host":"ru.example"}}}',2,1,'node','')`,
		`INSERT INTO clients VALUES(1,'alice','uuid-a',0,0,1,1,'','s1')`,
		`INSERT INTO client_inbounds VALUES(1,1)`,
		`INSERT INTO client_groups VALUES(1,'friends')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func TestImportDryRunPrintsThePlan(t *testing.T) {
	var out, errBuf strings.Builder
	if err := runImport([]string{"-db", importFixture(t), "-json"}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	var rep importpanel.Report
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("output is not the report: %v\n%s", err, out.String())
	}
	if len(rep.Inbounds) != 1 || len(rep.Clients) != 1 || len(rep.Groups) != 1 {
		t.Fatalf("plan %+v", rep)
	}
	if !strings.Contains(errBuf.String(), "run again with -apply") {
		t.Fatalf("stderr %q", errBuf.String())
	}
}

func TestImportApplySendsThePlanWithTheToken(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	var out, errBuf strings.Builder
	err := runImport([]string{
		"-db", importFixture(t), "-apply", "-master", srv.URL, "-token", "secret-token",
	}, &out, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"POST /inbounds/ru4", "PUT /clients", "POST /groups"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("%q missing from %v", want, got)
		}
	}
	if !strings.Contains(out.String(), "imported: 1 inbound(s)") {
		t.Fatalf("stdout %q", out.String())
	}
}
