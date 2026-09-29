package nodespec

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
		"balancer not yet":   func(s *Spec) { s.Exits[0].Hops = append(s.Exits[0].Hops, s.Exits[0].Hops[0]) },
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
