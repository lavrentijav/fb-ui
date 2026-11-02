package vnet

import (
	"hash/fnv"
	"maps"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// CascadeUser is the client other nodes use on a node's cascade inbound.
const CascadeUser = "servers"

// resolverPortBase is the first of the node's loopback DNS ports; the port
// follows the network id, so adding a network does not move the others.
const resolverPortBase = 15353

func resolverPort(network string, used map[int]bool) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(network))
	port := resolverPortBase + int(h.Sum32()%500)
	for used[port] {
		port++
	}
	used[port] = true
	return port
}

// Fill adds every network user to the spec's network inbounds and sets the
// table: members and their names, how to reach every other node, and the
// loopback resolvers of the networks that live on this node.
func (s *Store) Fill(spec *nodespec.Spec, specs map[string]nodespec.Spec) {
	users := s.Users()
	names := map[string]string{}
	for _, key := range s.State.Keys(NameTable) {
		if rec, ok := s.State.Get(NameTable, key); ok {
			names[str(rec, "user")] = key
		}
	}
	t := &nodespec.VNet{Peers: map[string]nodespec.Hop{}, Zone: nodespec.VNetZoneDefault}
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
	networks := map[string]bool{}
	for _, u := range users {
		if m, ok := s.NetworkOf(u.ID); ok {
			t.Members = append(t.Members, nodespec.VMember{
				Email: u.Email, Name: names[u.ID], Rev: RevEmail(u.Email), IP: u.IP, Network: m.Network,
			})
			networks[m.Network] = true
		}
	}
	used := map[int]bool{}
	for _, network := range slices.Sorted(maps.Keys(networks)) {
		t.Resolvers = append(t.Resolvers, nodespec.VResolver{Network: network, Port: resolverPort(network, used)})
	}
	for node, other := range specs {
		if node != spec.Node && other.VNetHop != nil {
			t.Peers[node] = *other.VNetHop
		}
	}
	spec.VNet = t
}
