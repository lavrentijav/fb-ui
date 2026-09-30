package xrayrun

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

func baseSpec() nodespec.Spec {
	return nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{
				Tag: "ru", Port: 12054, Network: "ws", Path: "/ws-ru", Host: "ru.example", Route: nodespec.Direct,
				Clients: []nodespec.Client{{Email: "alice", ID: "11111111-1111-1111-1111-111111111111"}},
			},
			{
				Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.example", Route: nodespec.Direct,
				Clients: []nodespec.Client{{Email: "alice", ID: "11111111-1111-1111-1111-111111111111"}},
			},
		},
		DirectAllow: []string{"10.0.0.0/16"},
	}
}

func xrayOf(t *testing.T, s nodespec.Spec) []byte {
	t.Helper()
	b, err := nodespec.Render(s)
	if err != nil {
		t.Fatal(err)
	}
	return b.Xray
}

func emailsIn(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var ib struct {
		Settings struct {
			Clients []struct {
				Email string `json:"email"`
			} `json:"clients"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &ib); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range ib.Settings.Clients {
		out = append(out, c.Email)
	}
	return out
}

func TestPlanHot(t *testing.T) {
	bob := nodespec.Client{Email: "bob", ID: "22222222-2222-2222-2222-222222222222"}
	tests := []struct {
		name   string
		change func(*nodespec.Spec)
		hot    bool
		check  func(t *testing.T, p hotPlan)
	}{
		{"nothing changed", func(*nodespec.Spec) {}, true, func(t *testing.T, p hotPlan) {
			if !p.empty() {
				t.Fatalf("plan for no change: %+v", p)
			}
		}},
		{
			"a user added", func(s *nodespec.Spec) { s.Inbounds[0].Clients = append(s.Inbounds[0].Clients, bob) }, true,
			func(t *testing.T, p hotPlan) {
				if len(p.addUsers) != 1 || !slices.Equal(emailsIn(t, p.addUsers[0]), []string{"bob"}) || tagOf(p.addUsers[0]) != "ru" {
					t.Fatalf("addUsers %s", p.addUsers)
				}
				if len(p.addInbounds)+len(p.rmInbounds)+len(p.rmUsers) != 0 || p.routing != nil {
					t.Fatalf("more than the user: %+v", p)
				}
			},
		},
		{"a user removed", func(s *nodespec.Spec) { s.Inbounds[1].Clients = nil }, true, func(t *testing.T, p hotPlan) {
			if !slices.Equal(p.rmUsers["de"], []string{"alice"}) || len(p.addUsers)+len(p.addInbounds) != 0 {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"a user's id changed", func(s *nodespec.Spec) { s.Inbounds[0].Clients[0].ID = bob.ID }, true, func(t *testing.T, p hotPlan) {
			if !slices.Equal(p.rmUsers["ru"], []string{"alice"}) || len(p.addUsers) != 1 {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"a route changed", func(s *nodespec.Spec) { s.Inbounds[1].Route = nodespec.Block }, true, func(t *testing.T, p hotPlan) {
			if p.routing == nil || len(p.addInbounds)+len(p.addOutbounds)+len(p.addUsers) != 0 {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"an inbound added", func(s *nodespec.Spec) {
			s.Inbounds = append(s.Inbounds, nodespec.Inbound{
				Tag: "x", Port: 12055, Network: "ws", Path: "/ws-x", Host: "ru.example",
				Route: nodespec.Direct, Clients: []nodespec.Client{bob},
			})
		}, true, func(t *testing.T, p hotPlan) {
			if len(p.addInbounds) != 1 || tagOf(p.addInbounds[0]) != "x" || len(p.rmInbounds) != 0 {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"an inbound's path changed", func(s *nodespec.Spec) { s.Inbounds[1].Path = "/ws-de2" }, true, func(t *testing.T, p hotPlan) {
			if !slices.Equal(p.rmInbounds, []string{"de"}) || len(p.addInbounds) != 1 || len(p.goneIns) != 0 {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"an inbound removed", func(s *nodespec.Spec) { s.Inbounds = s.Inbounds[:1] }, true, func(t *testing.T, p hotPlan) {
			if !slices.Equal(p.goneIns, []string{"de"}) {
				t.Fatalf("plan %+v", p)
			}
		}},
		{"the default outbound changed", func(s *nodespec.Spec) { s.DirectAllow = nil }, false, nil},
		{"the api moved", func(s *nodespec.Spec) { s.API = "127.0.0.1:10086" }, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := xrayOf(t, baseSpec())
			next := baseSpec()
			tt.change(&next)
			p, ok := planHot(cur, xrayOf(t, next))
			if ok != tt.hot {
				t.Fatalf("hot = %v, want %v", ok, tt.hot)
			}
			if ok {
				tt.check(t, p)
			}
		})
	}
}
