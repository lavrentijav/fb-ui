package outbound

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// Outbound is the wire shape of one Xray outbound. Field order is the order it
// serializes in, which subscription fixtures pin byte for byte.
type Outbound struct {
	Protocol       string               `json:"protocol"`
	Tag            string               `json:"tag"`
	StreamSettings json_util.RawMessage `json:"streamSettings"`
	Mux            json_util.RawMessage `json:"mux,omitempty"`
	Settings       map[string]any       `json:"settings,omitempty"`
}

type ServerSetting struct {
	Password string `json:"password"`
	Level    int    `json:"level"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Flow     string `json:"flow,omitempty"`
	Method   string `json:"method,omitempty"`
}

// Build renders the outbound that dials this inbound as this client. The
// address and port come from the inbound's Listen/Port, so a caller dialing it
// somewhere else (a subscription host, a cascade target) sets them on a copy
// first. Reports false for a protocol that has no such outbound shape here.
func Build(inbound *model.Inbound, client model.Client, streamSettings json_util.RawMessage, mux, tag string) (json_util.RawMessage, bool) {
	switch inbound.Protocol {
	case model.VMESS:
		return Vnext(inbound, streamSettings, client, mux, tag), true
	case model.VLESS:
		return Vless(inbound, streamSettings, client, mux, tag), true
	case model.Trojan, model.Shadowsocks:
		return Server(inbound, streamSettings, client, mux, tag), true
	}
	return nil, false
}

func Vnext(inbound *model.Inbound, streamSettings json_util.RawMessage, client model.Client, mux, tag string) json_util.RawMessage {
	outbound := Outbound{}

	outbound.Protocol = string(inbound.Protocol)
	outbound.Tag = tag
	if mux != "" {
		outbound.Mux = json_util.RawMessage(mux)
	}
	outbound.StreamSettings = streamSettings

	security := NormalizeVmessSecurity(client.Security)
	outbound.Settings = map[string]any{
		"address":  inbound.Listen,
		"port":     inbound.Port,
		"id":       client.ID,
		"security": security,
		"level":    8,
	}

	result, _ := json.MarshalIndent(outbound, "", "  ")
	return result
}

func Vless(inbound *model.Inbound, streamSettings json_util.RawMessage, client model.Client, mux, tag string) json_util.RawMessage {
	outbound := Outbound{}
	outbound.Protocol = string(inbound.Protocol)
	outbound.Tag = tag
	if mux != "" {
		outbound.Mux = json_util.RawMessage(mux)
	}
	outbound.StreamSettings = streamSettings

	// A VLESS inbound stores the peer value under "decryption"; an outbound must
	// carry a non-empty encryption or Xray refuses to load the config at all.
	inboundSettings := Settings(inbound)
	encryption, _ := inboundSettings["encryption"].(string)
	if encryption == "" {
		if decryption, ok := inboundSettings["decryption"].(string); ok && decryption != "" {
			encryption = decryption
		} else {
			encryption = "none"
		}
	}

	settings := map[string]any{
		"address":    inbound.Listen,
		"port":       inbound.Port,
		"id":         client.ID,
		"encryption": encryption,
		"level":      8,
	}
	if client.Flow != "" && !inbound.DisableFlow {
		settings["flow"] = client.Flow
	}
	outbound.Settings = settings
	result, _ := json.MarshalIndent(outbound, "", "  ")
	return result
}

func Server(inbound *model.Inbound, streamSettings json_util.RawMessage, client model.Client, mux, tag string) json_util.RawMessage {
	outbound := Outbound{}

	serverData := make([]ServerSetting, 1)
	serverData[0] = ServerSetting{
		Address:  inbound.Listen,
		Port:     inbound.Port,
		Level:    8,
		Password: client.Password,
	}

	if inbound.Protocol == model.Shadowsocks {
		inboundSettings := Settings(inbound)
		method, _ := inboundSettings["method"].(string)
		serverData[0].Method = method

		// server password in multi-user 2022 protocols
		if strings.HasPrefix(method, "2022") {
			if serverPassword, ok := inboundSettings["password"].(string); ok {
				serverData[0].Password = fmt.Sprintf("%s:%s", serverPassword, client.Password)
			}
		}
	}

	outbound.Protocol = string(inbound.Protocol)
	outbound.Tag = tag
	if mux != "" {
		outbound.Mux = json_util.RawMessage(mux)
	}
	outbound.StreamSettings = streamSettings

	// Wrap the endpoint in a "servers" array (the standard Xray schema for
	// Shadowsocks/Trojan outbounds). The flat top-level form only parses on very
	// recent xray-core; older bundled cores (e.g. in v2rayN) reject it, so SS
	// links fail to connect. See Vnext/Vless for the VMess/VLESS shape.
	server := map[string]any{
		"address":  serverData[0].Address,
		"port":     serverData[0].Port,
		"password": serverData[0].Password,
		"level":    8,
	}
	if inbound.Protocol == model.Shadowsocks {
		server["method"] = serverData[0].Method
	}
	outbound.Settings = map[string]any{
		"servers": []any{server},
	}

	result, _ := json.MarshalIndent(outbound, "", "  ")
	return result
}

func NormalizeVmessSecurity(security string) string {
	switch security {
	case "", "none", "zero":
		return "auto"
	}
	return security
}

// MergeFinalMask overlays the panel-wide finalmask onto an inbound's own: the
// tcp/udp mask lists concatenate, quicParams only fills in when absent.
func MergeFinalMask(base any, extra map[string]any) map[string]any {
	merged := map[string]any{}
	if baseMap, ok := base.(map[string]any); ok {
		for key, value := range baseMap {
			switch key {
			case "tcp", "udp":
				if masks, ok := value.([]any); ok {
					merged[key] = append([]any(nil), masks...)
				}
			default:
				merged[key] = value
			}
		}
	}

	for key, value := range extra {
		switch key {
		case "tcp", "udp":
			baseMasks, _ := merged[key].([]any)
			extraMasks, _ := value.([]any)
			if len(extraMasks) > 0 {
				merged[key] = append(baseMasks, extraMasks...)
			}
		case "quicParams":
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		default:
			merged[key] = value
		}
	}

	return merged
}
