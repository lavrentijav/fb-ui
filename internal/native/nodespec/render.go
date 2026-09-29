package nodespec

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/edge"
)

// Bundle is what the node receives: the Xray config as is, the edge table
// and the certificates the edge serves.
type Bundle struct {
	Xray json.RawMessage `json:"xray"`
	Edge Edge            `json:"edge"`
	// API is where the agent reads traffic from Xray.
	API   string `json:"api"`
	Certs []Cert `json:"certs,omitempty"`
}

// Edge is the node's 443 table: SNI routes, then HTTP routes for names the
// edge terminates itself (handoff "https").
type Edge struct {
	SNI  []SNIRoute  `json:"sni,omitempty"`
	HTTP []HTTPRoute `json:"http,omitempty"`
}

type SNIRoute struct {
	Match         string `json:"match"`
	Action        string `json:"action"` // tcp | handoff
	Target        string `json:"target"`
	ProxyProtocol int    `json:"proxyProtocol,omitempty"`
}

type HTTPRoute struct {
	Host       string `json:"host"`
	PathPrefix string `json:"pathPrefix"`
	Target     string `json:"target"`
}

// Handoff is the in-process TLS server the edge hands terminated names to.
const Handoff = "https"

type obj = map[string]any

// Render validates s and builds the node's bundle.
func Render(s Spec) (Bundle, error) {
	if err := s.Validate(); err != nil {
		return Bundle{}, err
	}
	api := s.API
	if api == "" {
		api = "127.0.0.1:10085"
	}
	apiHost, apiPort, err := splitHostPort(api)
	if err != nil {
		return Bundle{}, err
	}
	inbounds := []any{obj{
		"tag": "api", "listen": apiHost, "port": apiPort, "protocol": "tunnel",
		"settings": obj{"address": apiHost},
	}}
	rules := []any{obj{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}
	var edge Edge
	terminated := map[string]bool{}
	for _, ib := range s.Inbounds {
		clients := make([]any, 0, len(ib.Clients))
		for _, c := range ib.Clients {
			clients = append(clients, obj{"id": c.ID, "email": c.Email})
		}
		inbounds = append(inbounds, obj{
			"tag": ib.Tag, "listen": "127.0.0.1", "port": ib.Port, "protocol": "vless",
			"settings":       obj{"clients": clients, "decryption": "none"},
			"streamSettings": behindEdge(transport(ib.Network, ib.Path, "", "")),
			"sniffing":       obj{"enabled": true, "destOverride": []string{"http", "tls", "quic"}},
		})
		rules = append(rules, obj{"type": "field", "inboundTag": []string{ib.Tag}, "outboundTag": ib.Route})
		if ib.Host != "" {
			edge.HTTP = append(edge.HTTP, HTTPRoute{Host: ib.Host, PathPrefix: ib.Path, Target: "http://127.0.0.1:" + strconv.Itoa(ib.Port)})
			terminated[ib.Host] = true
		}
	}
	direct := obj{"tag": Direct, "protocol": "freedom"}
	if len(s.DirectAllow) > 0 {
		direct["settings"] = obj{"finalRules": []any{obj{"action": "allow", "ip": s.DirectAllow}}}
	}
	outbounds := []any{
		direct,
		obj{"tag": Block, "protocol": "blackhole"},
	}
	balanced := map[string]bool{}
	var balancers []any
	var observed []string
	for _, e := range s.Exits {
		tag := ProxyTag(e.Country)
		if len(e.Hops) == 1 {
			outbounds = append(outbounds, hopOutbound(tag, e.Hops[0]))
			continue
		}
		for i, h := range e.Hops {
			outbounds = append(outbounds, hopOutbound(fmt.Sprintf("%s-%d", tag, i+1), h))
		}
		balanced[tag] = true
		observed = append(observed, tag+"-")
		balancers = append(balancers, obj{
			"tag": tag, "selector": []string{tag + "-"},
			"strategy": obj{"type": "leastPing"}, "fallbackTag": tag + "-1",
		})
	}
	for _, r := range rules {
		if rule := r.(obj); balanced[fmt.Sprint(rule["outboundTag"])] {
			rule["balancerTag"] = rule["outboundTag"]
			delete(rule, "outboundTag")
		}
	}
	for _, r := range s.Reverse {
		switch r.Kind {
		case "tcp":
			edge.SNI = append(edge.SNI, SNIRoute{Match: r.SNI, Action: "tcp", Target: r.Target, ProxyProtocol: r.ProxyProtocol})
		case "http":
			edge.HTTP = append(edge.HTTP, HTTPRoute{Host: r.SNI, PathPrefix: r.Path, Target: r.Target})
			terminated[r.SNI] = true
		}
	}
	for name := range terminated {
		edge.SNI = append(edge.SNI, SNIRoute{Match: name, Action: "handoff", Target: Handoff})
	}
	// a stable table: an unchanged spec renders the same bundle and hash
	slices.SortFunc(edge.SNI, func(a, b SNIRoute) int { return strings.Compare(a.Match, b.Match) })
	cfg := obj{
		"log":       obj{"loglevel": "warning"},
		"api":       obj{"tag": "api", "services": []string{"StatsService", "HandlerService"}},
		"stats":     obj{},
		"policy":    obj{"levels": obj{"0": obj{"statsUserUplink": true, "statsUserDownlink": true}}},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   obj{"domainStrategy": "AsIs", "rules": rules},
	}
	if len(balancers) > 0 {
		cfg["routing"].(obj)["balancers"] = balancers
		probe := s.Probe
		if probe.URL == "" {
			probe.URL = "https://www.gstatic.com/generate_204"
		}
		if probe.Interval == "" {
			probe.Interval = "30s"
		}
		cfg["observatory"] = obj{
			"subjectSelector": observed, "probeURL": probe.URL,
			"probeInterval": probe.Interval, "enableConcurrency": true,
		}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Xray: raw, Edge: edge, API: api, Certs: s.Certs}, nil
}

// hopOutbound is a cascade to one exit node's inbound.
func hopOutbound(tag string, h Hop) obj {
	return obj{
		"tag": tag, "protocol": "vless",
		"settings": obj{"vnext": []any{obj{
			"address": h.Address, "port": h.Port,
			"users": []any{obj{"id": h.ID, "encryption": "none"}},
		}}},
		"streamSettings": transport(h.Network, h.Path, h.SNI, h.Host),
	}
}

// transport is streamSettings for ws/xhttp, with TLS when sni is set.
func transport(network, path, sni, host string) obj {
	st := obj{"network": network}
	switch network {
	case "ws":
		ws := obj{"path": path}
		if host != "" {
			ws["host"] = host
		}
		st["wsSettings"] = ws
	case "xhttp":
		xh := obj{"path": path}
		if host != "" {
			xh["host"] = host
		}
		st["xhttpSettings"] = xh
	}
	if sni != "" {
		st["security"] = "tls"
		st["tlsSettings"] = obj{"serverName": sni, "fingerprint": "chrome", "alpn": []string{"http/1.1"}}
	}
	return st
}

// behindEdge takes the client address from the edge's X-Forwarded-For.
func behindEdge(st obj) obj {
	st["sockopt"] = obj{"trustedXForwardedFor": []string{edge.TrustHeader}}
	return st
}

func splitHostPort(addr string) (string, int, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p, err := strconv.Atoi(addr[i+1:])
			if err != nil || p <= 0 || p > 65535 {
				break
			}
			return addr[:i], p, nil
		}
	}
	return "", 0, fmt.Errorf("nodespec: bad API address %q", addr)
}
