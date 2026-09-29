// Package nodespec is what a master tells a node to run, as a small typed
// spec; Render turns it into the Xray config and edge routes the node applies.
package nodespec

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
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
	// Exits are the proxy_<country> outbounds: cascades to exit nodes.
	Exits []Exit `json:"exits,omitempty"`
	// Reverse are edge routes to local services other than inbounds.
	Reverse []Reverse `json:"reverse,omitempty"`
	Certs   []Cert    `json:"certs,omitempty"`
	// DirectAllow are private CIDRs direct may reach; Xray blocks private
	// destinations by default (internal services, the lab network).
	DirectAllow []string `json:"directAllow,omitempty"`
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
}

type Client struct {
	Email string `json:"email"`
	ID    string `json:"id"`
}

// Exit leaves through another node (a cascade). One hop for now; several
// hops of one group become a balancer (see native-nodes.md).
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
		if len(e.Hops) != 1 {
			return fmt.Errorf("nodespec: exit %s has %d hops; balancing over a group is not built yet", e.Country, len(e.Hops))
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
