package masterd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/inboundparam"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// newTestDaemon is a Daemon over an in-memory projection.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{cfg: Config{Name: "fi1"}, store: master.NewMem()}
}

// set writes a record into the projection through one synthetic op per field.
// The sequence continues from what the store already applied, so several
// records can be seeded in one test.
func set(t *testing.T, d *Daemon, table, key string, fields map[string]any) {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw[k] = b
	}
	applied, err := d.store.Applied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seq := applied["fi1"] + 1
	if _, err := d.store.Apply(context.Background(), oplog.Op{
		Origin: "fi1", Seq: seq, Stamp: hlc.Stamp{Wall: int64(seq), Origin: "fi1"},
		Table: table, Key: key, Fields: raw,
	}); err != nil {
		t.Fatal(err)
	}
}

// seedFleet puts two nodes' specs, client records and usage into a test host.
func seedFleet(t *testing.T) *Daemon {
	t.Helper()
	d := newTestDaemon(t)
	ctx := context.Background()
	for node, ibs := range map[string][]nodespec.Inbound{
		"ru4": {{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: "ru.example",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: "u1"}},
		}},
		"de2": {{
			Tag: "xht", Port: 12055, Network: "xhttp", Path: "/xh", Host: "de.example",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: "u1"}, {Email: "bob", ID: "u2"}},
		}},
	} {
		raw, err := json.Marshal(nodespec.Spec{Node: node, Inbounds: ibs})
		if err != nil {
			t.Fatal(err)
		}
		set(t, d, SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
		_ = ctx
	}
	for _, c := range []clients.Client{
		{Node: "ru4", Inbound: "vpn", Email: "alice", UUID: "u1", Enable: true, TotalGB: 100, ExpiresAt: 2000, LimitIP: 2},
		{Node: "de2", Inbound: "xht", Email: "alice", UUID: "u1", Enable: true, TotalGB: 100, ExpiresAt: 2000, LimitIP: 2},
		{Node: "de2", Inbound: "xht", Email: "bob", UUID: "u2", Enable: false},
	} {
		set(t, d, clients.Table, clients.Key(c.Node, c.Inbound, c.Email), map[string]any{
			"node": c.Node, "inbound": c.Inbound, "email": c.Email, "uuid": c.UUID,
			"enable": c.Enable, "totalBytes": c.TotalGB, "expiresAt": c.ExpiresAt, "limitIp": c.LimitIP,
		})
	}
	set(t, d, usage.Table, "alice", map[string]any{"bytes": int64(4096)})
	set(t, d, subauth.Table, "tok-alice", map[string]any{
		"email": "alice", "user": "alice01", "enable": true,
	})
	return d
}

func TestFleetInboundsAggregateBothNodes(t *testing.T) {
	d := seedFleet(t)
	ins := d.fleetInbounds()
	if len(ins) != 2 {
		t.Fatalf("inbounds %d", len(ins))
	}
	seen := map[string]fleetInbound{}
	for _, in := range ins {
		seen[in.Tag] = in
		if in.ID <= 0 {
			t.Fatalf("inbound %s has a non-positive id %d", in.Tag, in.ID)
		}
		if in.Protocol != "vless" || in.Enable != true {
			t.Fatalf("inbound %+v", in)
		}
	}
	ru := seen["ru4/vpn"]
	if ru.Port != 12054 || ru.Down != 4096 || len(ru.ClientStats) != 1 || ru.ClientStats[0].Email != "alice" {
		t.Fatalf("ru4 %+v", ru)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(ru.StreamSettings), &st); err != nil || st["network"] != "ws" {
		t.Fatalf("stream %s %v", ru.StreamSettings, err)
	}
	de := seen["de2/xht"]
	if st := de.StreamSettings; !contains(st, `"xhttpSettings"`) {
		t.Fatalf("xhttp stream %s", st)
	}
	// ids are stable across calls
	if again := d.fleetInbounds(); again[0].ID != ins[0].ID || again[1].ID != ins[1].ID {
		t.Fatal("inbound ids are not stable")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The old interface's inbound list carries the inbound's custom params, so the
// sales bot can read the "reserved" flag and skip those inbounds.
func TestFleetInboundCarriesParams(t *testing.T) {
	d := newTestDaemon(t)
	raw, err := json.Marshal(nodespec.Spec{Node: "ru", Inbounds: []nodespec.Inbound{{
		Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: "ru.example",
		Route: nodespec.Direct,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	set(t, d, SpecTable, "ru", map[string]any{"spec": json.RawMessage(raw)})
	set(t, d, inboundparam.Table, inboundparam.Key("ru", "vpn"), map[string]any{"params": inboundparam.Params{"reserved": "true"}})

	ins := d.fleetInbounds()
	if len(ins) != 1 {
		t.Fatalf("inbounds %d", len(ins))
	}
	if !inboundparam.Reserved(ins[0].Params) {
		t.Fatalf("reserved params not exposed: %+v", ins[0].Params)
	}
}

func TestFleetClientsMergePerEmail(t *testing.T) {
	d := seedFleet(t)
	out := d.fleetClients()
	if len(out) != 2 {
		t.Fatalf("clients %+v", out)
	}
	byEmail := map[string]fleetClient{}
	for _, c := range out {
		byEmail[c.Email] = c
	}
	alice := byEmail["alice"]
	if len(alice.InboundIDs) != 2 || len(alice.Nodes) != 2 || alice.SubID != "tok-alice" {
		t.Fatalf("alice %+v", alice)
	}
	if alice.Traffic == nil || alice.Traffic.Down != 4096 {
		t.Fatalf("alice traffic %+v", alice.Traffic)
	}
	if byEmail["bob"].Enable {
		t.Fatalf("bob should be disabled: %+v", byEmail["bob"])
	}
}

func TestFleetClientsPagedSearchesAndPages(t *testing.T) {
	d := seedFleet(t)
	req := httptest.NewRequest(http.MethodGet, "/clients/list/paged?search=ali&page=1&pageSize=1", nil)
	page := d.fleetClientsPaged(req)
	if page["total"] != 2 || page["filtered"] != 1 || page["pageSize"] != 1 {
		t.Fatalf("page %+v", page)
	}
	items := page["items"].([]fleetClient)
	if len(items) != 1 || items[0].Email != "alice" {
		t.Fatalf("items %+v", items)
	}
}

// The old route reaches the master's aggregate handler through the compat
// fallback, which strips /panel/api.
func TestCompatFallbackReachesTheFleetView(t *testing.T) {
	d := seedFleet(t)
	h := withLegacyFallback(nil, http.NotFoundHandler(), d.Handler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/api/nodes/list", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nodes/list via compat: %d %s", rec.Code, rec.Body)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes %+v", nodes)
	}
}
