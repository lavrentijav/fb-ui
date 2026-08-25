package job

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func initPeerJobDB(t *testing.T) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

// seedJobPeer stores a peer pointing at srv. AllowPrivateAddress is required
// because a test server always listens on loopback, which the SSRF guard blocks.
func seedJobPeer(t *testing.T, srv *httptest.Server, status string) *model.Node {
	t.Helper()
	host, port, ok := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	if !ok {
		t.Fatalf("unexpected test server URL %q", srv.URL)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	peer := &model.Node{
		Name: "peer-1", Scheme: "http", SubDomain: host, SubPort: p, SubPath: "/sub/",
		Role: model.NodeRoleMaster, Enable: true, AllowPrivateAddress: true, Status: status,
	}
	if err := database.GetDB().Create(peer).Error; err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	return peer
}

func collectPeerEvents(t *testing.T) (<-chan eventbus.Event, func()) {
	t.Helper()
	bus := eventbus.New(0)
	events := make(chan eventbus.Event, 8)
	bus.Subscribe("peer-health-test", func(e eventbus.Event) {
		if e.Type == eventbus.EventPeerUp || e.Type == eventbus.EventPeerDown {
			events <- e
		}
	})
	previous := EventBus
	EventBus = bus
	return events, func() {
		EventBus = previous
		bus.Stop()
	}
}

func waitForPeerEvent(t *testing.T, events <-chan eventbus.Event) eventbus.Event {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a peer event")
		return eventbus.Event{}
	}
}

func TestPeerHealthJobMarksReachablePeerOnline(t *testing.T) {
	initPeerJobDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"alg":"ed25519","key":"deadbeef"}`))
	}))
	defer srv.Close()
	peer := seedJobPeer(t, srv, "unknown")

	events, restore := collectPeerEvents(t)
	defer restore()

	NewPeerHealthJob().Run()

	stored, err := (&service.PeerService{}).GetById(peer.Id)
	if err != nil {
		t.Fatalf("reload peer: %v", err)
	}
	if stored.Status != "online" {
		t.Fatalf("status = %q, want online (lastError=%q)", stored.Status, stored.LastError)
	}
	if stored.PublicKey != "deadbeef" {
		t.Fatalf("public key = %q, want the probed key", stored.PublicKey)
	}
	if e := waitForPeerEvent(t, events); e.Type != eventbus.EventPeerUp {
		t.Fatalf("event = %s, want peer.up", e.Type)
	}
}

func TestPeerHealthJobMarksUnreachablePeerOffline(t *testing.T) {
	initPeerJobDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	peer := seedJobPeer(t, srv, "online")

	events, restore := collectPeerEvents(t)
	defer restore()

	NewPeerHealthJob().Run()

	stored, err := (&service.PeerService{}).GetById(peer.Id)
	if err != nil {
		t.Fatalf("reload peer: %v", err)
	}
	if stored.Status != "offline" {
		t.Fatalf("status = %q, want offline", stored.Status)
	}
	if stored.LastError == "" {
		t.Fatal("lastError is empty for an unreachable peer")
	}
	if e := waitForPeerEvent(t, events); e.Type != eventbus.EventPeerDown {
		t.Fatalf("event = %s, want peer.down", e.Type)
	}
}

// A peer that was already offline must stay quiet: the transition guard is what
// stops an unreachable peer from paging the operator every 30 seconds.
func TestPeerHealthJobDoesNotRepeatDownEvents(t *testing.T) {
	initPeerJobDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	seedJobPeer(t, srv, "offline")

	events, restore := collectPeerEvents(t)
	defer restore()

	NewPeerHealthJob().Run()

	select {
	case e := <-events:
		t.Fatalf("published %s for a peer that was already offline", e.Type)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPeerHealthJobSkipsDisabledPeers(t *testing.T) {
	initPeerJobDB(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"alg":"ed25519","key":"deadbeef"}`))
	}))
	defer srv.Close()
	peer := seedJobPeer(t, srv, "unknown")
	if err := database.GetDB().Model(model.Node{}).Where("id = ?", peer.Id).Update("enable", false).Error; err != nil {
		t.Fatalf("disable peer: %v", err)
	}

	NewPeerHealthJob().Run()

	if hits != 0 {
		t.Fatalf("probed a disabled peer %d time(s)", hits)
	}
}
