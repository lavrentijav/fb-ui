package vnet_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const uid = "11111111-1111-4111-8111-111111111111"

func TestFillAddsUsersAndTable(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	s := st["fi1"]
	a, _ := s.CreateUser(ctx, "alice")
	_, _ = s.CreateUser(ctx, "bob")
	n, _ := s.CreateNetwork(ctx, "alice", "x")
	code, _ := s.Invite(ctx, "alice", time.Hour, 1)
	_ = s.Join(ctx, "bob", code)
	ru4 := nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{
		{Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct, VNet: true},
		{Tag: "cascade", Port: 2, Network: "ws", Path: "/c", Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: uid}}},
	}}
	de2 := nodespec.Spec{Node: "de2", VNetHop: &nodespec.Hop{Address: "de2.x", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.x"}}
	s.Fill(&ru4, map[string]nodespec.Spec{"ru4": ru4, "de2": de2})

	var alice nodespec.Client
	for _, c := range ru4.Inbounds[0].Clients {
		if c.Email == "alice" {
			alice = c
		}
	}
	if alice.ID != a.UUID || !alice.Reverse || len(ru4.Inbounds[0].Clients) != 2 {
		t.Fatalf("vpn clients %+v", ru4.Inbounds[0].Clients)
	}
	v := ru4.VNet
	if v == nil || v.Cascade != "cascade" || !slices.Equal(v.Entry, []string{"vpn"}) || v.Peers["de2"].Address != "de2.x" {
		t.Fatalf("table %+v", v)
	}
	if len(v.Members) != 2 || v.Members[0].Network != n {
		t.Fatalf("members %+v", v.Members)
	}
	if _, err := nodespec.Render(ru4); err != nil {
		t.Fatalf("filled spec does not render: %v", err)
	}
}
