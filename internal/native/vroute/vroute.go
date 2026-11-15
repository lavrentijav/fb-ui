// Package vroute turns a node's network table and the fleet's presence into
// Xray routing: into a member's own tunnel here, or over a cascade there.
package vroute

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

type obj = map[string]any

func PeerTag(node string) string { return "vnet-" + node }

// DNSTag is the freedom outbound delivering one network's DNS queries to
// its loopback resolver.
func DNSTag(network string) string { return "vnet-dns-" + network }

// Bans blocks every connection from the given source addresses before any
// other rule; an empty list leaves the config untouched.
func Bans(xray []byte, bans []string) ([]byte, error) {
	if len(bans) == 0 {
		return xray, nil
	}
	var cfg obj
	if err := json.Unmarshal(xray, &cfg); err != nil {
		return nil, err
	}
	routing, _ := cfg["routing"].(obj)
	old, _ := routing["rules"].([]any)
	rule := obj{
		"type": "field", "sourceIP": slices.Sorted(slices.Values(bans)),
		"outboundTag": nodespec.Block, "ruleTag": "bans",
	}
	merged := append([]any{}, old[:min(1, len(old))]...) // the api rule stays first
	merged = append(merged, rule)
	merged = append(merged, old[min(1, len(old)):]...)
	routing["rules"] = merged
	return json.MarshalIndent(cfg, "", "  ")
}

// Patch places the network rules right after the api rule, adds the
// cascades it uses, and blocks the rest of the network range.
func Patch(xray []byte, t *nodespec.VNet, where map[string]string, self string) ([]byte, error) {
	if t == nil {
		return xray, nil
	}
	var cfg obj
	if err := json.Unmarshal(xray, &cfg); err != nil {
		return nil, err
	}
	byNet := map[string][]string{}
	for _, m := range t.Members {
		byNet[m.Network] = append(byNet[m.Network], m.Email)
	}
	var rules []any
	peers := map[string]bool{}
	for _, m := range t.Members {
		users := slices.Sorted(slices.Values(byNet[m.Network]))
		rev := m.Rev
		if rev == "" {
			rev = m.Email
		}
		switch node := where[rev]; {
		case node == self:
			rules = append(rules,
				obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(rev)})
			if t.Cascade != "" {
				rules = append(rules,
					obj{"type": "field", "inboundTag": []string{t.Cascade}, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(rev)})
			}
		case node != "":
			if _, ok := t.Peers[node]; ok {
				peers[node] = true
				rules = append(rules,
					obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": PeerTag(node)})
			}
		}
	}
	// the resolver address is inside the network prefix: its rule must come
	// before the block below, so members of one network can resolve names
	dns := map[string]bool{}
	var dnsOuts []any
	for _, r := range t.Resolvers {
		users := slices.Sorted(slices.Values(byNet[r.Network]))
		if len(users) == 0 || r.Port <= 0 || dns[r.Network] {
			continue
		}
		dns[r.Network] = true
		rules = append(rules, obj{
			"type": "field", "inboundTag": t.Entry, "user": users,
			"ip": []string{nodespec.VDNSAddress}, "port": "53", "outboundTag": DNSTag(r.Network),
		})
		dnsOuts = append(dnsOuts, obj{
			"tag": DNSTag(r.Network), "protocol": "freedom",
			"settings": obj{
				"redirect":   "127.0.0.1:" + strconv.Itoa(r.Port),
				"finalRules": []any{obj{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
			},
		})
	}
	rules = append(rules, obj{"type": "field", "ip": []string{nodespec.VNetPrefix}, "outboundTag": nodespec.Block})

	routing, _ := cfg["routing"].(obj)
	old, _ := routing["rules"].([]any)
	merged := append([]any{}, old[:min(1, len(old))]...) // the api rule stays first
	merged = append(merged, rules...)
	merged = append(merged, old[min(1, len(old)):]...)
	routing["rules"] = merged

	outs, _ := cfg["outbounds"].([]any)
	for _, node := range slices.Sorted(maps.Keys(peers)) {
		outs = append(outs, nodespec.HopOutbound(PeerTag(node), t.Peers[node]))
	}
	outs = append(outs, dnsOuts...)
	cfg["outbounds"] = outs
	return json.MarshalIndent(cfg, "", "  ")
}
