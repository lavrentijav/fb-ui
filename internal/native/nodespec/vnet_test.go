package nodespec

import (
	"encoding/json"
	"strings"
	"testing"
)

const uid = "11111111-1111-4111-8111-111111111111"

func vnetSpec() Spec {
	return Spec{
		Node: "ru4",
		Inbounds: []Inbound{
			{
				Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Route: Direct, VNet: true,
				Clients: []Client{{Email: "alice", ID: uid, Reverse: true}},
			},
			{
				Tag: "cascade", Port: 12060, Network: "ws", Path: "/c", Route: Direct,
				Clients: []Client{{Email: "servers", ID: uid}},
			},
		},
		VNet: &VNet{
			Members: []VMember{{Email: "alice", Name: "alice", IP: "100.64.0.7", Network: "n1"}},
			Peers:   map[string]Hop{"de2": {Address: "de2.example", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.example"}},
			Entry:   []string{"vpn"}, Cascade: "cascade",
			Zone:      VNetZoneDefault,
			Resolvers: []VResolver{{Network: "n1", Port: 15353}},
		},
	}
}

func TestRenderReverseUsersAndOnlineStats(t *testing.T) {
	b, err := Render(vnetSpec())
	if err != nil {
		t.Fatal(err)
	}
	x := string(b.Xray)
	if !strings.Contains(x, `"tag": "u-alice"`) || !strings.Contains(x, `"statsUserOnline": true`) {
		t.Fatalf("xray config lacks the reverse tag or online stats:\n%s", x)
	}
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []map[string]any `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	_ = json.Unmarshal(b.Xray, &cfg)
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "cascade" && strings.Contains(fmtAny(ib.Settings.Clients), "reverse") {
			t.Fatal("a client without Reverse got a reverse tag")
		}
	}
	if b.VNet == nil || b.VNet.Cascade != "cascade" {
		t.Fatalf("bundle lost the network table: %+v", b.VNet)
	}
}

func fmtAny(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestValidateNetworkTable(t *testing.T) {
	for name, change := range map[string]func(*Spec){
		"address outside 100.64/10": func(s *Spec) { s.VNet.Members[0].IP = "10.0.0.7" },
		"unknown cascade inbound":   func(s *Spec) { s.VNet.Cascade = "nope" },
		"unknown entry inbound":     func(s *Spec) { s.VNet.Entry = []string{"nope"} },
		"peer without id":           func(s *Spec) { s.VNet.Peers["de2"] = Hop{Address: "x", Port: 443, Network: "ws", Path: "/c"} },
		"bad member name":           func(s *Spec) { s.VNet.Members[0].Name = "Alice." },
		"repeated member name":      func(s *Spec) { s.VNet.Members = append(s.VNet.Members, s.VNet.Members[0]) },
		"bad zone":                  func(s *Spec) { s.VNet.Zone = "local!" },
		"resolver without port":     func(s *Spec) { s.VNet.Resolvers[0].Port = 0 },
		"repeated resolver port":    func(s *Spec) { s.VNet.Resolvers = append(s.VNet.Resolvers, VResolver{Network: "n2", Port: 15353}) },
	} {
		t.Run(name, func(t *testing.T) {
			s := vnetSpec()
			change(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	s := vnetSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
}
