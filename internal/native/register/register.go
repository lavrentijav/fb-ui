// Package register turns a role and a public name into a node's spec: an
// entry (members' inbound plus a cascade to an exit) or an exit (a cascade
// inbound other nodes dial). It is pure, so the shape is unit-tested.
package register

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// ServersEmail is the client every node uses on an exit's cascade inbound.
const ServersEmail = "servers"

// Roles a node can be registered with.
const (
	RoleEntry = "entry"
	RoleExit  = "exit"
)

// Request is what an operator gives: who, what role, which public name, and
// for an entry where its traffic exits.
type Request struct {
	Node      string `json:"node"`
	Role      string `json:"role"`
	Host      string `json:"host"`      // public DNS name (the edge terminates it)
	Port      int    `json:"port"`      // loopback port of the inbound
	Path      string `json:"path"`      // ws path behind the edge
	Country   string `json:"country"`   // exit country tag, default "de"
	CascadeTo string `json:"cascadeTo"` // entry only: the exit node's name
	VNet      bool   `json:"vnet"`      // add members of virtual networks to the inbound
}

var validNode = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Build renders the spec. serversID is the fleet-wide cascade identity; peers
// are the specs already registered, so an entry can find its exit.
func Build(req Request, serversID string, peers map[string]nodespec.Spec) (nodespec.Spec, error) {
	if !validNode.MatchString(req.Node) {
		return nodespec.Spec{}, fmt.Errorf("register: bad node name %q", req.Node)
	}
	if req.Host == "" {
		return nodespec.Spec{}, errors.New("register: a public host is required")
	}
	if req.Port <= 0 {
		req.Port = 12054
	}
	if req.Path == "" {
		req.Path = "/ws"
	}
	if req.Country == "" {
		req.Country = "de"
	}
	spec := nodespec.Spec{Node: req.Node}
	switch req.Role {
	case RoleExit:
		spec.Inbounds = []nodespec.Inbound{{
			Tag: "cascade", Port: req.Port, Network: "ws", Path: req.Path, Host: req.Host,
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: ServersEmail, ID: serversID}},
		}}
		// other nodes reach this cascade through the edge, by name
		spec.VNetHop = &nodespec.Hop{
			Address: req.Host, Port: 443, ID: serversID,
			Network: "ws", Path: req.Path, SNI: req.Host, Host: req.Host,
		}
	case RoleEntry:
		route := nodespec.Direct
		if req.CascadeTo != "" {
			hop, err := exitHop(req.CascadeTo, peers)
			if err != nil {
				return nodespec.Spec{}, err
			}
			route = nodespec.ProxyTag(req.Country)
			spec.Exits = []nodespec.Exit{{Country: req.Country, Hops: []nodespec.Hop{hop}}}
		}
		spec.Inbounds = []nodespec.Inbound{{
			Tag: "vpn", Port: req.Port, Network: "ws", Path: req.Path, Host: req.Host,
			Route: route, VNet: req.VNet,
		}}
	default:
		return nodespec.Spec{}, fmt.Errorf("register: role must be %q or %q", RoleEntry, RoleExit)
	}
	if _, err := nodespec.Render(spec); err != nil {
		return nodespec.Spec{}, err
	}
	return spec, nil
}

// exitHop finds the exit node's cascade inbound and how the edge exposes it.
func exitHop(node string, peers map[string]nodespec.Spec) (nodespec.Hop, error) {
	peer, ok := peers[node]
	if !ok {
		return nodespec.Hop{}, fmt.Errorf("register: no spec for exit %q", node)
	}
	for _, ib := range peer.Inbounds {
		if ib.Tag != "cascade" || ib.Host == "" {
			continue
		}
		for _, c := range ib.Clients {
			if c.Email == ServersEmail {
				return nodespec.Hop{
					Address: ib.Host, Port: 443, ID: c.ID,
					Network: ib.Network, Path: ib.Path, SNI: ib.Host, Host: ib.Host,
				}, nil
			}
		}
	}
	return nodespec.Hop{}, fmt.Errorf("register: exit %q has no cascade inbound with a public host", node)
}

// Encode is the JSON stored in the op log.
func Encode(s nodespec.Spec) (json.RawMessage, error) { return json.Marshal(s) }

// Describe is a short human summary for the answer.
func Describe(s nodespec.Spec) string {
	var b strings.Builder
	b.WriteString(s.Node)
	for _, ib := range s.Inbounds {
		fmt.Fprintf(&b, " %s->%s", ib.Tag, ib.Route)
	}
	for _, e := range s.Exits {
		fmt.Fprintf(&b, " exit:%s", e.Country)
	}
	return b.String()
}

// NewServersID is the fleet-wide cascade identity, created once.
func NewServersID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
