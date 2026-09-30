// Package vroute turns a node's network table and the fleet's presence into
// Xray routing: into a member's own tunnel here, or over a cascade there.
package vroute

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

type obj = map[string]any

func PeerTag(node string) string { return "vnet-" + node }

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
		switch node := where[m.Email]; {
		case node == self:
			rules = append(rules,
				obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(m.Email)})
			if t.Cascade != "" {
				rules = append(rules,
					obj{"type": "field", "inboundTag": []string{t.Cascade}, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(m.Email)})
			}
		case node != "":
			if _, ok := t.Peers[node]; ok {
				peers[node] = true
				rules = append(rules,
					obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": PeerTag(node)})
			}
		}
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
	cfg["outbounds"] = outs
	return json.MarshalIndent(cfg, "", "  ")
}
