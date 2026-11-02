// Package sub renders a client's subscription links from the nodes' specs;
// it has no state and no dependency on a master.
package sub

import (
	"encoding/base64"
	"net/url"
	"slices"
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

// LinksWith is Links plus the hosts map.
func LinksWith(specs []nodespec.Spec, email string, hosts Hosts) []string {
	specs = slices.Clone(specs)
	slices.SortFunc(specs, func(a, b nodespec.Spec) int { return strings.Compare(a.Node, b.Node) })
	var out []string
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
				adv := hosts[s.Node+"/"+ib.Tag]
				if len(adv) == 0 {
					out = append(out, Link(c.ID, Endpoint{
						Address: ib.Host, Path: ib.Path, Remark: base,
					}))
					continue
				}
				for _, e := range adv {
					if e.Path == "" {
						e.Path = ib.Path
					}
					e.Remark = joinRemark(base, e.Remark)
					out = append(out, Link(c.ID, e))
				}
			}
		}
	}
	return out
}

// Endpoint is one way to reach an inbound: the address a client dials and
// the transport parameters its link carries.
type Endpoint struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`     // 0 = 443
	Security string `json:"security"` // default tls
	SNI      string `json:"sni"`      // default: Address
	Host     string `json:"host"`     // ws Host header, default: SNI
	Path     string `json:"path"`     // default: the inbound's path
	ALPN     string `json:"alpn"`     // default http/1.1 when TLS
	Remark   string `json:"remark"`   // appended to the link remark
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

// Link is a VLESS ws link for one endpoint.
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
	q.Set("encryption", "none")
	q.Set("security", security)
	q.Set("type", "ws")
	q.Set("path", e.Path)
	q.Set("host", host)
	if security != "none" {
		q.Set("sni", sni)
		alpn := e.ALPN
		if alpn == "" {
			alpn = "http/1.1"
		}
		q.Set("alpn", alpn)
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
