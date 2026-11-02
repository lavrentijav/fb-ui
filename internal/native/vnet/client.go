package vnet

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// ClientEntry is the node (or group name) the client connects to.
type ClientEntry struct {
	Address       string
	Port          int
	SNI, Host     string
	Network, Path string
}

// ClientConfig is a member's Xray config: its tunnel carries the network
// both ways; what the node sends back lands on the device's own ports.
func ClientConfig(u User, e ClientEntry, inbounds []map[string]any) ([]byte, error) {
	hop := nodespec.HopOutbound("vpn", nodespec.Hop{
		Address: e.Address, Port: e.Port, ID: u.UUID, Network: e.Network, Path: e.Path, SNI: e.SNI, Host: e.Host,
	})
	// the flat VLESS form is the one the lab's reverse test proved on Xray 26
	vpn := map[string]any{
		"tag": "vpn", "protocol": "vless", "streamSettings": hop["streamSettings"],
		"settings": map[string]any{
			"address": e.Address, "port": e.Port, "id": u.UUID, "email": u.Email, "encryption": "none",
		},
	}
	revID := u.RevUUID
	if revID == "" {
		revID = u.UUID
	}
	rev := map[string]any{
		"tag": "rev", "protocol": "vless", "streamSettings": hop["streamSettings"],
		"settings": map[string]any{
			"address": e.Address, "port": e.Port, "id": revID, "email": RevEmail(u.Email), "encryption": "none",
			"reverse": map[string]any{"tag": "from-node"},
		},
	}
	local := map[string]any{
		"tag": "local", "protocol": "freedom",
		"settings": map[string]any{
			"redirect":   "127.0.0.1:0",
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
		},
	}
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": []any{vpn, rev, local},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"from-node"}, "outboundTag": "local"},
			map[string]any{"type": "field", "ip": []string{nodespec.VNetPrefix}, "outboundTag": "vpn"},
		}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
