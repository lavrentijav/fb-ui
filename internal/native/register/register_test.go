package register_test

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/register"
)

const serversID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

func exit(t *testing.T) nodespec.Spec {
	t.Helper()
	s, err := register.Build(register.Request{
		Node: "de2", Role: register.RoleExit, Host: "de2.example", Port: 12060, Path: "/ws-net",
	}, serversID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExitServesACascade(t *testing.T) {
	s := exit(t)
	if len(s.Inbounds) != 1 || s.Inbounds[0].Tag != "cascade" || s.Inbounds[0].Route != nodespec.Direct {
		t.Fatalf("exit spec %+v", s)
	}
	if len(s.Inbounds[0].Clients) != 1 || s.Inbounds[0].Clients[0].Email != register.ServersEmail {
		t.Fatalf("cascade clients %+v", s.Inbounds[0].Clients)
	}
	if s.VNetHop == nil || s.VNetHop.Address != "de2.example" || s.VNetHop.Port != 443 {
		t.Fatalf("hop %+v", s.VNetHop)
	}
}

func TestEntryCascadesToTheExit(t *testing.T) {
	peers := map[string]nodespec.Spec{"de2": exit(t)}
	s, err := register.Build(register.Request{
		Node: "ru4", Role: register.RoleEntry, Host: "ru.example", Port: 12054, Path: "/ws",
		CascadeTo: "de2", Country: "de", VNet: true,
	}, serversID, peers)
	if err != nil {
		t.Fatal(err)
	}
	ib := s.Inbounds[0]
	if ib.Tag != "vpn" || ib.Route != nodespec.ProxyTag("de") || !ib.VNet {
		t.Fatalf("entry inbound %+v", ib)
	}
	if len(s.Exits) != 1 || s.Exits[0].Country != "de" || len(s.Exits[0].Hops) != 1 {
		t.Fatalf("exits %+v", s.Exits)
	}
	h := s.Exits[0].Hops[0]
	if h.Address != "de2.example" || h.ID != serversID || h.Path != "/ws-net" || h.Port != 443 {
		t.Fatalf("hop %+v", h)
	}
	if _, err := nodespec.Render(s); err != nil {
		t.Fatalf("spec does not render: %v", err)
	}
}

func TestEntryWithoutExitIsDirect(t *testing.T) {
	s, err := register.Build(register.Request{Node: "ru4", Role: register.RoleEntry, Host: "ru.example"}, serversID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Inbounds[0].Route != nodespec.Direct || len(s.Exits) != 0 {
		t.Fatalf("entry spec %+v", s)
	}
}

func TestRefusals(t *testing.T) {
	for name, req := range map[string]register.Request{
		"no host":      {Node: "ru4", Role: register.RoleEntry},
		"bad node":     {Node: "RU4!", Role: register.RoleEntry, Host: "x"},
		"bad role":     {Node: "ru4", Role: "middle", Host: "x"},
		"unknown exit": {Node: "ru4", Role: register.RoleEntry, Host: "x", CascadeTo: "nl9"},
	} {
		if _, err := register.Build(req, serversID, nil); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	// an exit without a public host cannot be dialled by name
	peers := map[string]nodespec.Spec{"de2": {Node: "de2", Inbounds: []nodespec.Inbound{{
		Tag: "cascade", Port: 1, Network: "ws", Path: "/c", Route: nodespec.Direct,
		Clients: []nodespec.Client{{Email: register.ServersEmail, ID: serversID}},
	}}}}
	if _, err := register.Build(register.Request{Node: "ru4", Role: register.RoleEntry, Host: "x", CascadeTo: "de2"},
		serversID, peers); err == nil || !strings.Contains(err.Error(), "public host") {
		t.Fatalf("a hostless exit: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	peers := map[string]nodespec.Spec{"de2": exit(t)}
	s, _ := register.Build(register.Request{Node: "ru4", Role: register.RoleEntry, Host: "ru.example", CascadeTo: "de2"},
		serversID, peers)
	if got := register.Describe(s); got != "ru4 vpn->proxy_de exit:de" {
		t.Fatalf("describe %q", got)
	}
}
