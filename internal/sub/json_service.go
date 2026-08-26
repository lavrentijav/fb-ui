package sub

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
	xrayout "github.com/mhsanaei/3x-ui/v3/internal/xray/outbound"
)

//go:embed default.json
var defaultJson string

// SubJsonService handles JSON subscription configuration generation and management.
type SubJsonService struct {
	configJson       map[string]any
	defaultOutbounds []json_util.RawMessage
	finalMask        string
	mux              string

	SubService *SubService
}

// NewSubJsonService creates a new JSON subscription service with the given configuration.
func NewSubJsonService(mux string, rules string, finalMask string, subService *SubService) *SubJsonService {
	var configJson map[string]any
	var defaultOutbounds []json_util.RawMessage
	_ = json.Unmarshal([]byte(defaultJson), &configJson)
	if outboundSlices, ok := configJson["outbounds"].([]any); ok {
		for _, defaultOutbound := range outboundSlices {
			jsonBytes, _ := json.Marshal(defaultOutbound)
			defaultOutbounds = append(defaultOutbounds, jsonBytes)
		}
	}

	if rules != "" {
		var newRules []any
		routing, _ := configJson["routing"].(map[string]any)
		defaultRules, _ := routing["rules"].([]any)
		_ = json.Unmarshal([]byte(rules), &newRules)
		defaultRules = append(newRules, defaultRules...)
		routing["rules"] = defaultRules
		configJson["routing"] = routing
	}

	return &SubJsonService{
		configJson:       configJson,
		defaultOutbounds: defaultOutbounds,
		finalMask:        finalMask,
		mux:              mux,
		SubService:       subService,
	}
}

// GetJson generates a JSON subscription configuration for the given subscription ID and host.
func (s *SubJsonService) GetJson(subId string, host string, alwaysReturnArray bool) (string, string, error) {
	subReq := s.SubService.ForRequest(host)
	subReq.subscriptionBody = true
	inbounds, err := subReq.getInboundsBySubId(subId)
	if err != nil {
		return "", "", err
	}
	externalLinks, err := subReq.getClientExternalLinksBySubId(subId)
	if err != nil {
		return "", "", err
	}
	if len(inbounds) == 0 && len(externalLinks) == 0 {
		return "", "", nil
	}

	var header string
	var configArray []json_util.RawMessage

	seenEmails := make(map[string]struct{})
	// Prepare Inbounds
	for _, inbound := range inbounds {
		clients := subReq.matchingClients(inbound, subId)
		if len(clients) == 0 {
			continue
		}
		subReq.projectThroughFallbackMaster(inbound)
		if hostEps := subReq.hostEndpoints(inbound, "json"); len(hostEps) > 0 {
			injectExternalProxy(inbound, hostEps)
		}

		for _, client := range clients {
			seenEmails[client.Email] = struct{}{}
			configArray = append(configArray, s.getConfig(subReq, inbound, client, host)...)
		}
	}
	for _, ext := range externalLinks {
		for _, el := range expandEntry(ext) {
			outbound := parsedExternalOutbound(el.Link)
			if outbound == nil {
				continue
			}
			seenEmails[ext.Email] = struct{}{}
			remark := el.Name
			if remark == "" {
				remark = ext.Email
			}
			newOutbounds := []json_util.RawMessage{outbound}
			newOutbounds = append(newOutbounds, s.defaultOutbounds...)
			newConfigJson := make(map[string]any)
			maps.Copy(newConfigJson, s.configJson)
			newConfigJson["outbounds"] = newOutbounds
			newConfigJson["remarks"] = remark
			newConfig, _ := json.MarshalIndent(newConfigJson, "", "  ")
			configArray = append(configArray, newConfig)
		}
	}

	if len(configArray) == 0 {
		return "", "", nil
	}

	emails := make([]string, 0, len(seenEmails))
	for e := range seenEmails {
		emails = append(emails, e)
	}
	traffic, _ := subReq.AggregateTrafficByEmails(emails)

	var finalJson []byte
	if len(configArray) == 1 && !alwaysReturnArray {
		finalJson, _ = json.MarshalIndent(configArray[0], "", "  ")
	} else {
		finalJson, _ = json.MarshalIndent(configArray, "", "  ")
	}

	header = fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", traffic.Up, traffic.Down, traffic.Total, traffic.ExpiryTime/1000)
	return string(finalJson), header, nil
}

func (s *SubJsonService) getConfig(subReq *SubService, inbound *model.Inbound, client model.Client, host string) []json_util.RawMessage {
	var newJsonArray []json_util.RawMessage
	stream := xrayout.Stream(inbound.StreamSettings, xrayout.ClientKey(client), s.finalMask)

	// When externalProxy is empty the JSON config falls back to a
	// synthetic one whose `dest` is the host the client connects to.
	// For node-managed inbounds we want the node's address — request
	// host won't reach the right xray. resolveInboundAddress already
	// implements the node→subscriber-host fallback chain.
	defaultDest := subReq.resolveInboundAddress(inbound)
	if defaultDest == "" {
		defaultDest = host
	}

	// Per-inbound xmux takes precedence over the global subJsonMux.
	// When xmux is present inside xhttpSettings, XHTTP multiplexing
	// is handled by xmux — don't also set the legacy outbound.Mux.
	mux := s.mux
	if xhttp, ok := stream["xhttpSettings"].(map[string]any); ok {
		if _, hasXmux := xhttp["xmux"]; hasXmux {
			mux = ""
		}
	}

	externalProxies, ok := stream["externalProxy"].([]any)
	hasExternalProxy := ok && len(externalProxies) > 0
	if !hasExternalProxy {
		externalProxies = []any{
			map[string]any{
				"forceTls": "same",
				"dest":     defaultDest,
				"port":     float64(inbound.Port),
				"remark":   "",
			},
		}
	}

	delete(stream, "externalProxy")
	network, _ := stream["network"].(string)

	for _, ep := range externalProxies {
		extPrxy, ok := ep.(map[string]any)
		if !ok {
			continue
		}
		// Expand the host's {{VAR}} remark template for this client (no-op for
		// the synthetic/legacy entry) before it's used as the config remark.
		subReq.renderHostRemark(inbound, client, extPrxy, network)
		inbound.Listen, _ = extPrxy["dest"].(string)
		if port, ok := extPrxy["port"].(float64); ok {
			inbound.Port = int(port)
		}
		newStream := cloneStreamForExternalProxy(stream)
		forceTls, _ := extPrxy["forceTls"].(string)
		switch forceTls {
		case "tls":
			if newStream["security"] != "tls" {
				newStream["security"] = "tls"
				newStream["tlsSettings"] = map[string]any{}
			}
		case "none":
			if newStream["security"] != "none" {
				newStream["security"] = "none"
				delete(newStream, "tlsSettings")
			}
		}
		security, _ := newStream["security"].(string)
		if hasExternalProxy {
			applyExternalProxyTLSToStream(extPrxy, newStream, security)
		}
		applyHostStreamOverrides(extPrxy, newStream)
		streamSettings, _ := json.MarshalIndent(newStream, "", "  ")
		hostMux := hostMuxOverride(extPrxy)

		var newOutbounds []json_util.RawMessage

		switch inbound.Protocol {
		case "vmess":
			newOutbounds = append(newOutbounds, xrayout.Vnext(inbound, streamSettings, client, jsonMux(mux, hostMux), proxyOutboundTag))
		case "vless":
			vc := client
			vc.ID = applyVlessRoute(client.ID, hostVlessRoute(extPrxy))
			// Same gate the raw link and the Clash proxy apply: a flow left
			// over from a transport Vision supported produces an outbound
			// xray refuses to start.
			newNetwork, _ := newStream["network"].(string)
			if vc.Flow != "" && !vlessFlowAllowed(newNetwork, security, subReq.linkSettings(inbound)) {
				vc.Flow = ""
			}
			newOutbounds = append(newOutbounds, xrayout.Vless(inbound, streamSettings, vc, jsonMux(mux, hostMux), proxyOutboundTag))
		case "trojan", "shadowsocks":
			newOutbounds = append(newOutbounds, xrayout.Server(inbound, streamSettings, client, jsonMux(mux, hostMux), proxyOutboundTag))
		case "hysteria":
			newOutbounds = append(newOutbounds, s.genHy(inbound, newStream, client, jsonMux(mux, hostMux)))
		case "wireguard":
			wgOutbound := s.genWireguard(inbound, client)
			if wgOutbound == nil {
				continue
			}
			newOutbounds = append(newOutbounds, wgOutbound)
		}

		newOutbounds = append(newOutbounds, s.defaultOutbounds...)
		newConfigJson := make(map[string]any)
		maps.Copy(newConfigJson, s.configJson)

		transport, _ := newStream["network"].(string)
		newConfigJson["outbounds"] = newOutbounds
		newConfigJson["remarks"] = subReq.endpointRemark(inbound, client.Email, extPrxy, transport)

		newConfig, _ := json.MarshalIndent(newConfigJson, "", "  ")
		newJsonArray = append(newJsonArray, newConfig)
	}

	return newJsonArray
}

// proxyOutboundTag is the tag every subscription outbound carries; clients
// key their own routing off it.
const proxyOutboundTag = "proxy"

// jsonMux picks the per-host mux override when present, else the global mux.
func jsonMux(global, override string) string {
	if override != "" {
		return override
	}
	return global
}

func (s *SubJsonService) genHy(inbound *model.Inbound, newStream map[string]any, client model.Client, mux string) json_util.RawMessage {
	outbound := xrayout.Outbound{}

	outbound.Protocol = string(inbound.Protocol)
	outbound.Tag = "proxy"

	if mux != "" {
		outbound.Mux = json_util.RawMessage(mux)
	}

	var settings, stream map[string]any
	_ = json.Unmarshal([]byte(inbound.Settings), &settings)
	version, _ := settings["version"].(float64)
	outbound.Settings = map[string]any{
		"version": int(version),
		"address": inbound.Listen,
		"port":    inbound.Port,
	}

	_ = json.Unmarshal([]byte(inbound.StreamSettings), &stream)
	hyStream, _ := stream["hysteriaSettings"].(map[string]any)
	outHyStream := map[string]any{
		"version": int(version),
		"auth":    client.Auth,
	}
	if udpIdleTimeout, ok := hyStream["udpIdleTimeout"].(float64); ok {
		outHyStream["udpIdleTimeout"] = int(udpIdleTimeout)
	}
	if masquerade, ok := hyStream["masquerade"].(map[string]any); ok {
		outHyStream["masquerade"] = masquerade
	}
	newStream["hysteriaSettings"] = outHyStream

	if finalmask, ok := hyStream["finalmask"].(map[string]any); ok {
		newStream["finalmask"] = xrayout.MergeFinalMask(newStream["finalmask"], finalmask)
	}

	newStream["network"] = "hysteria"
	newStream["security"] = "tls"

	outbound.StreamSettings, _ = json.MarshalIndent(newStream, "", "  ")

	result, _ := json.MarshalIndent(outbound, "", "  ")
	return result
}

// genWireguard builds an Xray wireguard outbound for a native WireGuard inbound,
// mirroring genWireguardLink: the peer public key is derived from the inbound
// secretKey, the client owns the private key / tunnel address / pre-shared key,
// and the peer routes the full tunnel. Returns nil when the client has no key.
func (s *SubJsonService) genWireguard(inbound *model.Inbound, client model.Client) json_util.RawMessage {
	if client.PrivateKey == "" {
		return nil
	}

	var inboundSettings map[string]any
	_ = json.Unmarshal([]byte(inbound.Settings), &inboundSettings)
	secretKey, _ := inboundSettings["secretKey"].(string)

	peer := map[string]any{
		"endpoint":   joinHostPort(inbound.Listen, inbound.Port),
		"allowedIPs": []string{"0.0.0.0/0", "::/0"},
	}
	if secretKey != "" {
		if pub, err := wgutil.PublicKeyFromPrivate(secretKey); err == nil {
			peer["publicKey"] = pub
		}
	}
	if client.PreSharedKey != "" {
		peer["preSharedKey"] = client.PreSharedKey
	}
	if client.KeepAlive > 0 {
		peer["keepAlive"] = client.KeepAlive
	}

	settings := map[string]any{
		"secretKey": client.PrivateKey,
		"peers":     []any{peer},
	}
	if len(client.AllowedIPs) > 0 {
		settings["address"] = client.AllowedIPs
	}
	if mtu, ok := inboundSettings["mtu"].(float64); ok && mtu > 0 {
		settings["mtu"] = int(mtu)
	}

	outbound := map[string]any{
		"protocol": string(inbound.Protocol),
		"tag":      "proxy",
		"settings": settings,
	}
	result, _ := json.MarshalIndent(outbound, "", "  ")
	return result
}
