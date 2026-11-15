// Package sub renders a client's subscription links from the nodes' specs;
// it has no state and no dependency on a master.
package sub

import (
	"encoding/base64"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// Hosts is a node+inbound's advertised endpoints, keyed "node/inbound"; a
// non-empty list replaces the default endpoint, so a subscription can hand
// out a CDN name, a spare address or a different port.
type Hosts map[string][]Endpoint

// Links is every way the client with this email can connect: one link per
// inbound with a public name (or per advertised endpoint), across all nodes,
// in a stable order.
func Links(specs []nodespec.Spec, email string) []string {
	return LinksWith(specs, email, nil)
}

// Options tune a link list: the advertised endpoints, the remark each link
// carries, and extra parameters (identity, per-server quota).
type Options struct {
	Hosts Hosts
	// Remark names a link; nil keeps the "<node>-<inbound>" default. An empty
	// answer falls back to the default too.
	Remark func(node string, ib nodespec.Inbound) string
	// Extra adds query parameters to every link of an inbound.
	Extra func(node string, ib nodespec.Inbound) map[string]string
}

// LinksWith is Links plus the hosts map.
func LinksWith(specs []nodespec.Spec, email string, hosts Hosts) []string {
	return LinksWithOpts(specs, email, Options{Hosts: hosts})
}

// LinksWithOpts is Links with the subscription's own naming and extras.
func LinksWithOpts(specs []nodespec.Spec, email string, opt Options) []string {
	specs = slices.Clone(specs)
	slices.SortFunc(specs, func(a, b nodespec.Spec) int { return strings.Compare(a.Node, b.Node) })
	var out []string
	// one link per dialable endpoint, not per node: a multicast pair advertises
	// the same name/path, so its members must collapse to a single link
	seen := map[string]bool{}
	emit := func(id string, e Endpoint) {
		key := id + "\x00" + e.identity()
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Link(id, e))
	}
	for _, s := range specs {
		for _, ib := range s.Inbounds {
			if ib.Host == "" {
				continue
			}
			for _, c := range ib.Clients {
				if c.Email != email || c.Reverse {
					continue
				}
				base := s.Node + "-" + ib.Tag
				remark := base
				if opt.Remark != nil {
					if named := strings.TrimSpace(opt.Remark(s.Node, ib)); named != "" {
						remark = named
					}
				}
				var extra map[string]string
				if opt.Extra != nil {
					extra = opt.Extra(s.Node, ib)
				}
				adv := opt.Hosts[s.Node+"/"+ib.Tag]
				if len(adv) == 0 {
					emit(c.ID, Endpoint{
						Address: ib.Host, Network: ib.Network, Path: ib.Path,
						Remark: remark, Extra: extra,
					})
					continue
				}
				for _, e := range adv {
					if e.Path == "" {
						e.Path = ib.Path
					}
					if e.Network == "" {
						e.Network = ib.Network
					}
					e.Remark = joinRemark(remark, e.Remark)
					e.Extra = merge(extra, e.Extra)
					emit(c.ID, e)
				}
			}
		}
	}
	return out
}

// merge combines two parameter maps; the endpoint's own values win.
func merge(base, own map[string]string) map[string]string {
	if len(own) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(own))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}

// Endpoint is one way to reach an inbound: the address a client dials and
// the transport parameters its link carries.
type Endpoint struct {
	Address     string `json:"address"`
	Port        int    `json:"port"`        // 0 = 443
	Network     string `json:"network"`     // ws | xhttp (default ws)
	Security    string `json:"security"`    // default tls
	SNI         string `json:"sni"`         // default: Address
	Host        string `json:"host"`        // ws Host header, default: SNI
	Path        string `json:"path"`        // default: the inbound's path
	ALPN        string `json:"alpn"`        // default http/1.1 when TLS
	Fingerprint string `json:"fingerprint"` // uTLS profile, default chrome
	Remark      string `json:"remark"`      // appended to the link remark
	// Extra are additional query parameters. Unknown ones are ignored by
	// every surveyed client, so this is how a subscription carries more than
	// the link strictly needs (userinfo, providerid, node metadata).
	Extra map[string]string `json:"extra,omitempty"`
}

func (e Endpoint) port() int {
	if e.Port > 0 {
		return e.Port
	}
	return 443
}

func (e Endpoint) security() string {
	if e.Security == "" {
		return "tls"
	}
	return e.Security
}

func (e Endpoint) network() string {
	if e.Network != "" {
		return e.Network
	}
	return "ws"
}

func (e Endpoint) fingerprint() string {
	if e.Fingerprint != "" {
		return e.Fingerprint
	}
	return "chrome"
}

// identity is what a client actually dials, minus the link-only extras (node,
// userinfo) and the remark, so the members of one multicast collapse to a key.
func (e Endpoint) identity() string {
	sni := e.SNI
	if sni == "" {
		sni = e.Address
	}
	host := e.Host
	if host == "" {
		host = sni
	}
	return strings.Join([]string{
		e.Address, strconv.Itoa(e.port()), e.network(), e.Path, e.security(), sni, host,
	}, "\x00")
}

// Link is a VLESS link for one endpoint. Extra parameters are added first, so
// a caller cannot overwrite what the transport itself requires.
func Link(id string, e Endpoint) string {
	security := e.security()
	sni := e.SNI
	if sni == "" {
		sni = e.Address
	}
	host := e.Host
	if host == "" {
		host = sni
	}
	q := url.Values{}
	for k, v := range e.Extra {
		if v != "" {
			q.Set(k, v)
		}
	}
	q.Set("encryption", "none")
	q.Set("security", security)
	q.Set("type", e.network())
	q.Set("path", e.Path)
	q.Set("host", host)
	q.Set("headerType", "none")
	if security != "none" {
		q.Set("sni", sni)
		q.Set("fp", e.fingerprint())
		alpn := e.ALPN
		if alpn == "" {
			alpn = "http/1.1"
		}
		q.Set("alpn", alpn)
		q.Set("allowInsecure", "0")
	}
	remark := strings.TrimSpace(e.Remark)
	return "vless://" + id + "@" + e.Address + ":" + itoa(e.port()) + "?" + q.Encode() + "#" + escape(remark)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func joinRemark(base, extra string) string {
	if extra == "" {
		return base
	}
	return base + " " + extra
}

func escape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// Base64 is the common subscription body: one link per line, base64-encoded.
func Base64(links []string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
}
