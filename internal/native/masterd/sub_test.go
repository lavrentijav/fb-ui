package masterd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// The public subscription path serves one VLESS link per inbound that carries
// the client: ws on ru4 and xhttp on de2 here. This is the same render the
// interface and the node sharing a spec use, without starting a bus or Xray.
func TestSubscriptionServesVlessWsAndXhttpLinks(t *testing.T) {
	d := seedFleet(t)
	req := httptest.NewRequest(http.MethodGet, "/sub/tok-alice?format=plain", nil)
	req.SetPathValue("token", "tok-alice")
	rec := httptest.NewRecorder()
	d.serveSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sub: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if got := strings.Count(body, "vless://"); got != 2 {
		t.Fatalf("%d links in:\n%s", got, body)
	}
	for _, want := range []string{"ru.example", "de.example", "type=ws", "type=xhttp", "security=tls", "path=%2Fws"} {
		if !strings.Contains(body, want) {
			t.Fatalf("%q missing from:\n%s", want, body)
		}
	}
	if h := rec.Header().Get("subscription-userinfo"); !strings.Contains(h, "total=100") {
		t.Fatalf("subscription-userinfo %q", h)
	}

	// the default body is base64, one link per line, for the older clients
	req = httptest.NewRequest(http.MethodGet, "/sub/tok-alice", nil)
	req.SetPathValue("token", "tok-alice")
	rec = httptest.NewRecorder()
	d.serveSub(rec, req)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("base64 body: %v (%q)", err, rec.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines %v", lines)
	}
	for _, line := range lines {
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "vless" || u.User.Username() == "" {
			t.Fatalf("link %q: %v", line, err)
		}
	}
}

// The inbound's remark (the old panel's friendly name) must survive into the
// link's #fragment; losing it is the "names flew off" cutover bug.
func TestSubscriptionLinkCarriesInboundRemark(t *testing.T) {
	d := newTestDaemon(t)
	const remark = "🇩🇪 Германия Франкфурт"
	raw, err := json.Marshal(nodespec.Spec{Node: "ru", Inbounds: []nodespec.Inbound{{
		Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: "ru.example",
		Route: nodespec.Direct, Remark: remark,
		Clients: []nodespec.Client{{Email: "alice", ID: "u1"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	set(t, d, SpecTable, "ru", map[string]any{"spec": json.RawMessage(raw)})
	set(t, d, subauth.Table, "tok-alice", map[string]any{"email": "alice", "user": "alice01", "enable": true})

	req := httptest.NewRequest(http.MethodGet, "/sub/tok-alice?format=plain", nil)
	req.SetPathValue("token", "tok-alice")
	rec := httptest.NewRecorder()
	d.serveSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sub: %d %s", rec.Code, rec.Body)
	}
	want := "#" + strings.ReplaceAll(url.QueryEscape(remark), "+", "%20")
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("remark fragment %q missing from:\n%s", want, rec.Body.String())
	}
}

// A multicast pair advertises the same endpoint; the deduped link must carry the
// sum of both nodes' traffic, not one node's share.
func TestMulticastLinksSumTraffic(t *testing.T) {
	d := newTestDaemon(t)
	addSpec := func(node, tag string, port int) {
		raw, err := json.Marshal(nodespec.Spec{Node: node, Inbounds: []nodespec.Inbound{{
			Tag: tag, Port: port, Network: "ws", Path: "/auto", Host: "ru.example",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: "u1"}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		set(t, d, SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	}
	addSpec("ru", "a", 12055)
	addSpec("ru1", "b", 12056)
	set(t, d, usage.NodeTable, "alice/ru", map[string]any{"up": int64(100), "down": int64(200)})
	set(t, d, usage.NodeTable, "alice/ru1", map[string]any{"up": int64(50), "down": int64(25)})
	set(t, d, subauth.Table, "tok-alice", map[string]any{"email": "alice", "user": "alice01", "enable": true})

	req := httptest.NewRequest(http.MethodGet, "/sub/tok-alice?format=plain", nil)
	req.SetPathValue("token", "tok-alice")
	rec := httptest.NewRecorder()
	d.serveSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sub: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if got := strings.Count(body, "vless://"); got != 1 {
		t.Fatalf("a multicast pair must dedup to one link, got %d:\n%s", got, body)
	}
	// userinfo is the aggregate of both nodes: up 100+50, down 200+25
	if !strings.Contains(body, "upload%3D150") || !strings.Contains(body, "download%3D225") {
		t.Fatalf("userinfo must sum both nodes, got:\n%s", body)
	}
}
