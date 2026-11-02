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

// Links is every way the client with this email can connect: one link per
// inbound with a public name, across all nodes, in a stable order.
func Links(specs []nodespec.Spec, email string) []string {
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
				out = append(out, Link(c.ID, ib.Host, ib.Path, s.Node+"-"+ib.Tag))
			}
		}
	}
	return out
}

// Link is a VLESS link to a node's edge (443, ws behind TLS).
func Link(id, host, path, remark string) string {
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "tls")
	q.Set("sni", host)
	q.Set("type", "ws")
	q.Set("host", host)
	q.Set("path", path)
	q.Set("alpn", "http/1.1")
	return "vless://" + id + "@" + host + ":443?" + q.Encode() + "#" + escape(remark)
}

func escape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// Base64 is the common subscription body: one link per line, base64-encoded.
func Base64(links []string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
}
