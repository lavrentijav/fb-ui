package nodespec

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/sidecar"
)

const (
	aliceID   = "11111111-2222-4333-8444-555555555555"
	serversID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

// ru entry: a WS inbound per country behind the edge, cascades to exits.
func entrySpec() Spec {
	return Spec{
		Node: "ru4",
		Inbounds: []Inbound{
			{Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.firewalk.top", Route: ProxyTag("de"), Clients: []Client{{Email: "alice", ID: aliceID}}},
			{Tag: "local", Port: 12099, Network: "ws", Path: "/ws-ru", Host: "ru.firewalk.top", Route: Direct, Clients: []Client{{Email: "alice", ID: aliceID}}},
		},
		Exits: []Exit{{Country: "de", Hops: []Hop{{Address: "de2.firewalk.top", Port: 443, ID: serversID, Network: "ws", Path: "/ws-update", SNI: "de2.firewalk.top", Host: "de2.firewalk.top"}}}},
		Reverse: []Reverse{
			{Kind: "tcp", SNI: "mail.fire-core.ru", Target: "127.0.0.1:10443", ProxyProtocol: 2},
			{Kind: "http", SNI: "ru.firewalk.top", Path: "/walksub/", Target: "http://127.0.0.1:12099"},
		},
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Spec){
		"unknown route":      func(s *Spec) { s.Inbounds[0].Route = "proxy_fr" },
		"repeated port":      func(s *Spec) { s.Inbounds[1].Port = s.Inbounds[0].Port },
		"repeated tag":       func(s *Spec) { s.Inbounds[1].Tag = "de" },
		"api tag taken":      func(s *Spec) { s.Inbounds[0].Tag = "api" },
		"bad client uuid":    func(s *Spec) { s.Inbounds[0].Clients[0].ID = "nope" },
		"stats separator":    func(s *Spec) { s.Inbounds[0].Clients[0].Email = "a>>>b" },
		"repeated client":    func(s *Spec) { s.Inbounds[0].Clients = append(s.Inbounds[0].Clients, s.Inbounds[0].Clients[0]) },
		"relative path":      func(s *Spec) { s.Inbounds[0].Path = "ws-de" },
		"grpc not supported": func(s *Spec) { s.Inbounds[0].Network = "grpc" },
		"exit twice":         func(s *Spec) { s.Exits = append(s.Exits, s.Exits[0]) },
		"exit without hops":  func(s *Spec) { s.Exits[0].Hops = nil },
		"probe too often":    func(s *Spec) { s.Probe.Interval = "100ms" },
		"bad reverse kind":   func(s *Spec) { s.Reverse[0].Kind = "udp" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := entrySpec()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	s := entrySpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestRenderEntryNode(t *testing.T) {
	b, err := Render(entrySpec())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []struct {
			Tag            string          `json:"tag"`
			StreamSettings json.RawMessage `json:"streamSettings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(b.Xray, &cfg); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, o := range cfg.Outbounds {
		tags = append(tags, o.Tag)
	}
	if strings.Join(tags, ",") != "direct,block,proxy_de" {
		t.Fatalf("outbounds %v", tags)
	}
	var st bytes.Buffer
	_ = json.Compact(&st, cfg.Outbounds[2].StreamSettings)
	if !strings.Contains(st.String(), `"serverName":"de2.firewalk.top"`) {
		t.Fatalf("cascade without TLS to de2: %s", cfg.Outbounds[2].StreamSettings)
	}
	routes := map[string]string{}
	for _, r := range cfg.Routing.Rules {
		routes[r.InboundTag[0]] = r.OutboundTag
	}
	if routes["de"] != "proxy_de" || routes["local"] != "direct" || routes["api"] != "api" {
		t.Fatalf("routing %v", routes)
	}
	wantSNI := []SNIRoute{
		{Match: "mail.fire-core.ru", Action: "tcp", Target: "127.0.0.1:10443", ProxyProtocol: 2},
		{Match: "ru.firewalk.top", Action: "handoff", Target: Handoff},
	}
	if len(b.Edge.SNI) != 2 || b.Edge.SNI[0] != wantSNI[0] || b.Edge.SNI[1] != wantSNI[1] {
		t.Fatalf("edge SNI %+v", b.Edge.SNI)
	}
	if len(b.Edge.HTTP) != 3 || b.Edge.HTTP[0] != (HTTPRoute{Host: "ru.firewalk.top", PathPrefix: "/ws-de", Target: "http://127.0.0.1:12053"}) {
		t.Fatalf("edge HTTP %+v", b.Edge.HTTP)
	}
	again, _ := Render(entrySpec())
	if !bytes.Equal(again.Xray, b.Xray) {
		t.Fatal("render is not deterministic")
	}
}

// Exit nodes of one group behind one proxy_de: one outbound per hop, a
// leastPing balancer the routing points at, and the observatory probing it.
func TestRenderGroupBalancer(t *testing.T) {
	s := entrySpec()
	de1 := s.Exits[0].Hops[0]
	de1.Address, de1.SNI, de1.Host = "de1.firewalk.top", "de1.firewalk.top", "de1.firewalk.top"
	s.Exits[0].Hops = append(s.Exits[0].Hops, de1)
	s.Probe = Probe{URL: "http://probe.example/204", Interval: "5s"}
	b, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []struct{ Tag string } `json:"outbounds"`
		Routing   struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
				BalancerTag string   `json:"balancerTag"`
			} `json:"rules"`
			Balancers []struct {
				Tag         string                `json:"tag"`
				Selector    []string              `json:"selector"`
				FallbackTag string                `json:"fallbackTag"`
				Strategy    struct{ Type string } `json:"strategy"`
			} `json:"balancers"`
		} `json:"routing"`
		Observatory struct {
			SubjectSelector []string `json:"subjectSelector"`
			ProbeURL        string   `json:"probeURL"`
			ProbeInterval   string   `json:"probeInterval"`
		} `json:"observatory"`
	}
	if err := json.Unmarshal(b.Xray, &cfg); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, o := range cfg.Outbounds {
		tags = append(tags, o.Tag)
	}
	if strings.Join(tags, ",") != "direct,block,proxy_de-1,proxy_de-2" {
		t.Fatalf("outbounds %v", tags)
	}
	bal := cfg.Routing.Balancers
	if len(bal) != 1 || bal[0].Tag != "proxy_de" || bal[0].Selector[0] != "proxy_de-" || bal[0].Strategy.Type != "leastPing" || bal[0].FallbackTag != "proxy_de-1" {
		t.Fatalf("balancers %+v", bal)
	}
	for _, r := range cfg.Routing.Rules {
		if r.InboundTag[0] == "de" && (r.BalancerTag != "proxy_de" || r.OutboundTag != "") {
			t.Fatalf("de routes to %+v, want the balancer", r)
		}
	}
	o := cfg.Observatory
	if len(o.SubjectSelector) != 1 || o.SubjectSelector[0] != "proxy_de-" || o.ProbeURL != "http://probe.example/204" || o.ProbeInterval != "5s" {
		t.Fatalf("observatory %+v", o)
	}
}

func TestBundleCarriesTheStrictestIPLimit(t *testing.T) {
	s := Spec{Node: "ru4", Inbounds: []Inbound{
		{Tag: "a", Port: 1, Network: "ws", Path: "/a", Route: Direct, Clients: []Client{{Email: "alice", ID: uid, LimitIP: 3}}},
		{Tag: "b", Port: 2, Network: "ws", Path: "/b", Route: Direct, Clients: []Client{{Email: "alice", ID: uid, LimitIP: 1}, {Email: "bob", ID: uid}}},
	}}
	b, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if b.Limits["alice"] != 1 || b.Limits["bob"] != 0 {
		t.Fatalf("limits %v", b.Limits)
	}
	s.Inbounds[0].Clients[0].LimitIP = -1
	if _, err := Render(s); err == nil {
		t.Fatal("a negative limit was accepted")
	}
}

// The bundle carries each user's allowance and expiry so the node can stop a
// client locally; several records for one email resolve to the strictest.
func TestBundleCarriesQuotas(t *testing.T) {
	s := Spec{Node: "ru4", Inbounds: []Inbound{
		{Tag: "a", Port: 1, Network: "ws", Path: "/a", Route: Direct, Clients: []Client{
			{Email: "alice", ID: uid, TotalBytes: 100, ExpiresAt: 2000},
		}},
		{Tag: "b", Port: 2, Network: "ws", Path: "/b", Route: Direct, Clients: []Client{
			{Email: "alice", ID: uid, TotalBytes: 50, ExpiresAt: 1000},
			{Email: "bob", ID: uid},
		}},
	}}
	b, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if q := b.Quotas["alice"]; q.TotalBytes != 50 || q.ExpiresAt != 1000 {
		t.Fatalf("alice quota %+v", q)
	}
	if _, ok := b.Quotas["bob"]; ok {
		t.Fatalf("an unlimited client got a quota: %+v", b.Quotas)
	}
}

func TestBundleCarriesSidecarsAndValidatesThem(t *testing.T) {
	s := Spec{Node: "ru4", Inbounds: []Inbound{
		{Tag: "a", Port: 1, Network: "ws", Path: "/a", Route: Direct},
	}, Sidecars: []sidecar.Spec{{
		Name: "tuic", Binary: "/usr/local/bin/tuic", Args: []string{"-c", "/etc/tuic.json"},
		ConfigPath: "/etc/tuic.json", Config: []byte(`{}`),
	}}}
	b, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sidecars) != 1 || b.Sidecars[0].Name != "tuic" || b.Sidecars[0].Binary != "/usr/local/bin/tuic" {
		t.Fatalf("sidecars %+v", b.Sidecars)
	}
	// a bad sidecar fails the render instead of reaching the node
	s.Sidecars[0].Binary = "relative"
	if _, err := Render(s); err == nil {
		t.Fatal("a relative sidecar binary passed validation")
	}
}
