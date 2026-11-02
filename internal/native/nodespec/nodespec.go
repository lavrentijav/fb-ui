// Package nodespec is what a master tells a node to run, as a small typed
// spec; Render turns it into the Xray config and edge routes the node applies.
package nodespec

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Built-in outbounds every node has.
const (
	Direct = "direct"
	Block  = "block"
)

// ProxyTag is the outbound that leaves through the exit nodes of a country.
func ProxyTag(country string) string { return "proxy_" + country }

// Spec is one node's whole desired setup.
type Spec struct {
	Node string `json:"node"`
	// API is where Xray's stats API listens (default 127.0.0.1:10085).
	API      string    `json:"api,omitempty"`
	Inbounds []Inbound `json:"inbounds"`
	// Rules are matched in order before an inbound's own Route, so a rule
	// overrides the default for the traffic it names.
	Rules []Rule `json:"rules,omitempty"`
	// Exits are the proxy_<country> outbounds: cascades to exit nodes.
	Exits []Exit `json:"exits,omitempty"`
	// Reverse are edge routes to local services other than inbounds.
	Reverse []Reverse `json:"reverse,omitempty"`
	Certs   []Cert    `json:"certs,omitempty"`
	// CertIDs name certificates the masters issue and renew; a master
	// adds them to Certs when it renders the bundle.
	CertIDs []string `json:"certIds,omitempty"`
	// DirectAllow are private CIDRs direct may reach; Xray blocks private
	// destinations by default (internal services, the lab network).
	DirectAllow []string `json:"directAllow,omitempty"`
	// Probe checks the hops of balanced exits (defaults below).
	Probe Probe `json:"probe,omitzero"`
	// Drain puts the node in maintenance: the edge refuses new tunnels and
	// lets open ones finish (closing those idle longer than Idle).
	Drain Drain `json:"drain,omitzero"`
	// VNetHop is how other nodes reach this node's cascade for network
	// traffic; VNet is filled by the master.
	VNetHop *Hop  `json:"vnetHop,omitempty"`
	VNet    *VNet `json:"vnet,omitempty"`
}

type Drain struct {
	On   bool   `json:"on"`
	Idle string `json:"idle,omitempty"` // default 2m
}

// Probe is how Xray's observatory checks each hop of a balanced exit.
type Probe struct {
	URL      string `json:"url,omitempty"`      // default https://www.gstatic.com/generate_204
	Interval string `json:"interval,omitempty"` // default 30s
}

// Inbound is a VLESS inbound on loopback behind the edge: the edge
// terminates TLS for Host and forwards Path to it.
type Inbound struct {
	Tag     string   `json:"tag"`
	Port    int      `json:"port"`
	Network string   `json:"network"` // ws | xhttp
	Path    string   `json:"path"`
	Host    string   `json:"host,omitempty"` // public name; "" = not exposed on the edge
	Clients []Client `json:"clients"`
	// Route is the outbound for its traffic: direct, block or proxy_<country>.
	Route string `json:"route"`
	// VNet: the masters add every network user to this inbound.
	VNet bool `json:"vnet,omitempty"`
}

// Rule sends traffic matching Domains or IPs (Xray matcher syntax) to Route:
// direct, block or proxy_<country>. Inbounds narrows it to those inbound tags;
// empty means every inbound. Traffic no rule names follows the inbound's Route.
type Rule struct {
	Name     string   `json:"name,omitempty"`
	Inbounds []string `json:"inbounds,omitempty"`
	Domains  []string `json:"domains,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	Route    string   `json:"route"`
}

type Client struct {
	Email string `json:"email"`
	ID    string `json:"id"`
	// Reverse lets the client take connections from the node (its own tunnel).
	Reverse bool `json:"reverse,omitempty"`
}

// ReverseTag is the node's outbound into a user's reverse tunnel.
func ReverseTag(email string) string { return "u-" + email }

// VNet is the virtual network table a master renders for a node: members,
// how to reach other nodes, which inbounds carry members and cascades.
type VNet struct {
	Members []VMember      `json:"members"`
	Peers   map[string]Hop `json:"peers,omitempty"`
	Entry   []string       `json:"entry"`
	Cascade string         `json:"cascade,omitempty"`
}

type VMember struct {
	Email string `json:"email"`
	// Rev is the reverse-tunnel user's email on the same inbound; empty
	// means Email (older tables).
	Rev     string `json:"rev,omitempty"`
	IP      string `json:"ip"`
	Network string `json:"network"`
}

// VNetPrefix holds every member address.
const VNetPrefix = "100.64.0.0/10"

// Exit leaves through another node (a cascade). Several hops (the exit
// nodes of one group) become a balancer that prefers the fastest alive hop.
type Exit struct {
	Country string `json:"country"`
	Hops    []Hop  `json:"hops"`
}

// Hop is an exit node's cascade inbound as seen from here.
type Hop struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	ID      string `json:"id"` // the cascade client's UUID on the exit node
	Network string `json:"network"`
	Path    string `json:"path"`
	SNI     string `json:"sni,omitempty"` // "" = no TLS (only for tests and private links)
	Host    string `json:"host,omitempty"`
}

// Reverse routes an edge name to a local service: Kind "tcp" splices TLS
// untouched to Target (host:port); "http" terminates TLS and proxies
// Host+Path to Target (an http(s) URL).
type Reverse struct {
	Kind   string `json:"kind"`
	SNI    string `json:"sni"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target"`
	// ProxyProtocol sends a PROXY header (1 or 2) to a tcp Target.
	ProxyProtocol int `json:"proxyProtocol,omitempty"`
}

// Cert is a certificate the edge serves for Names.
type Cert struct {
	Names   []string `json:"names"`
	CertPEM string   `json:"certPem"`
	KeyPEM  string   `json:"keyPem"`
}

var (
	validTag     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	validCountry = regexp.MustCompile(`^[a-z]{2}[0-9]?$`)
	validUUID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Validate rejects a spec the node could not run as meant.
func (s *Spec) Validate() error {
	if s.Node == "" {
		return fmt.Errorf("nodespec: node name missing")
	}
	outs := []string{Direct, Block}
	for _, e := range s.Exits {
		if !validCountry.MatchString(e.Country) {
			return fmt.Errorf("nodespec: bad exit country %q", e.Country)
		}
		if slices.Contains(outs, ProxyTag(e.Country)) {
			return fmt.Errorf("nodespec: exit %s twice", e.Country)
		}
		if len(e.Hops) == 0 {
			return fmt.Errorf("nodespec: exit %s has no hops", e.Country)
		}
		for _, h := range e.Hops {
			if h.Address == "" || h.Port <= 0 || h.Port > 65535 || !validUUID.MatchString(h.ID) {
				return fmt.Errorf("nodespec: exit %s: bad hop %s:%d", e.Country, h.Address, h.Port)
			}
			if err := checkTransport(h.Network, h.Path); err != nil {
				return fmt.Errorf("nodespec: exit %s: %w", e.Country, err)
			}
		}
		outs = append(outs, ProxyTag(e.Country))
	}
	tags, ports := map[string]bool{"api": true}, map[int]bool{}
	for _, ib := range s.Inbounds {
		switch {
		case !validTag.MatchString(ib.Tag) || tags[ib.Tag]:
			return fmt.Errorf("nodespec: bad or repeated inbound tag %q", ib.Tag)
		case ib.Port <= 0 || ib.Port > 65535 || ports[ib.Port]:
			return fmt.Errorf("nodespec: inbound %s: bad or repeated port %d", ib.Tag, ib.Port)
		case !slices.Contains(outs, ib.Route):
			return fmt.Errorf("nodespec: inbound %s routes to unknown %q", ib.Tag, ib.Route)
		}
		if err := checkTransport(ib.Network, ib.Path); err != nil {
			return fmt.Errorf("nodespec: inbound %s: %w", ib.Tag, err)
		}
		emails := map[string]bool{}
		for _, c := range ib.Clients {
			if c.Email == "" || emails[c.Email] || !validUUID.MatchString(c.ID) || strings.Contains(c.Email, ">>>") {
				return fmt.Errorf("nodespec: inbound %s: bad or repeated client %q", ib.Tag, c.Email)
			}
			emails[c.Email] = true
		}
		tags[ib.Tag], ports[ib.Port] = true, true
	}
	for i, r := range s.Rules {
		if !slices.Contains(outs, r.Route) {
			return fmt.Errorf("nodespec: rule %d routes to unknown %q", i, r.Route)
		}
		if len(r.Domains)+len(r.IPs) == 0 {
			return fmt.Errorf("nodespec: rule %d matches nothing", i)
		}
		for _, m := range append(slices.Clone(r.Domains), r.IPs...) {
			if m == "" {
				return fmt.Errorf("nodespec: rule %d has an empty matcher", i)
			}
		}
		for _, tag := range r.Inbounds {
			if tag == "api" || !tags[tag] {
				return fmt.Errorf("nodespec: rule %d names unknown inbound %q", i, tag)
			}
		}
	}
	if s.Probe.Interval != "" {
		if d, err := time.ParseDuration(s.Probe.Interval); err != nil || d < time.Second {
			return fmt.Errorf("nodespec: probe interval %q", s.Probe.Interval)
		}
	}
	if s.Drain.Idle != "" {
		if d, err := time.ParseDuration(s.Drain.Idle); err != nil || d < time.Second {
			return fmt.Errorf("nodespec: drain idle %q", s.Drain.Idle)
		}
	}
	for _, cidr := range s.DirectAllow {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("nodespec: directAllow: %w", err)
		}
	}
	for _, r := range s.Reverse {
		if r.SNI == "" || r.Target == "" || (r.Kind != "tcp" && r.Kind != "http") {
			return fmt.Errorf("nodespec: bad reverse route %+v", r)
		}
	}
	if v := s.VNet; v != nil {
		prefix := netip.MustParsePrefix(VNetPrefix)
		for _, m := range v.Members {
			ip, err := netip.ParseAddr(m.IP)
			if m.Email == "" || m.Network == "" || err != nil || !prefix.Contains(ip) {
				return fmt.Errorf("nodespec: bad network member %+v", m)
			}
		}
		for _, tag := range append(slices.Clone(v.Entry), v.Cascade) {
			if tag != "" && !tags[tag] {
				return fmt.Errorf("nodespec: network inbound %q is not an inbound", tag)
			}
		}
		for node, h := range v.Peers {
			if h.Address == "" || h.Port <= 0 || !validUUID.MatchString(h.ID) || checkTransport(h.Network, h.Path) != nil {
				return fmt.Errorf("nodespec: bad network peer %s", node)
			}
		}
	}
	return nil
}

func checkTransport(network, path string) error {
	if network != "ws" && network != "xhttp" {
		return fmt.Errorf("network %q not supported", network)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path %q must start with /", path)
	}
	return nil
}
