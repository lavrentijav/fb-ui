package vnet

import (
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// CascadeUser is the client other nodes use on a node's cascade inbound.
const CascadeUser = "servers"

// Fill adds every network user to the spec's network inbounds and sets the
// table: members of every network and how to reach every other node.
func (s *Store) Fill(spec *nodespec.Spec, specs map[string]nodespec.Spec) {
	users := s.Users()
	t := &nodespec.VNet{Peers: map[string]nodespec.Hop{}}
	for i := range spec.Inbounds {
		ib := &spec.Inbounds[i]
		if ib.VNet {
			t.Entry = append(t.Entry, ib.Tag)
			for _, u := range users {
				ib.Clients = append(ib.Clients, nodespec.Client{Email: u.Email, ID: u.UUID})
				if u.RevUUID != "" {
					ib.Clients = append(ib.Clients,
						nodespec.Client{Email: RevEmail(u.Email), ID: u.RevUUID, Reverse: true})
				}
			}
		}
		if slices.ContainsFunc(ib.Clients, func(c nodespec.Client) bool { return c.Email == CascadeUser }) {
			t.Cascade = ib.Tag
		}
	}
	if len(t.Entry) == 0 {
		return
	}
	for _, u := range users {
		if m, ok := s.NetworkOf(u.ID); ok {
			t.Members = append(t.Members, nodespec.VMember{Email: u.Email, Rev: RevEmail(u.Email), IP: u.IP, Network: m.Network})
		}
	}
	for node, other := range specs {
		if node != spec.Node && other.VNetHop != nil {
			t.Peers[node] = *other.VNetHop
		}
	}
	spec.VNet = t
}
