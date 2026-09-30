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
			Members: []VMember{{Email: "alice", IP: "100.64.0.7", Network: "n1"}},
			Peers:   map[string]Hop{"de2": {Address: "de2.example", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.example"}},
			Entry:   []string{"vpn"}, Cascade: "cascade",
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
