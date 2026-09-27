package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	xrayout "github.com/mhsanaei/3x-ui/v3/internal/xray/outbound"
)

// buildCascadeOutbound renders the outbound that dials the link's target: the
// same client-side view of an inbound a subscription hands a user's app, only
// this panel is the one dialing. Reports false when the target cannot be
// described — the link then stays pending instead of routing nowhere.
func buildCascadeOutbound(link *model.CascadeLink) (json_util.RawMessage, bool) {
	db := database.GetDB()

	target := &model.Inbound{}
	if err := db.Where("id = ?", link.TargetInboundId).First(target).Error; err != nil {
		logger.Warning("cascade outbound: target inbound of link ", link.Id, " is gone")
		return nil, false
	}
	if panelOf(target) != link.TargetPanelId {
		logger.Warning("cascade outbound: target inbound of link ", link.Id, " no longer lives on its panel")
		return nil, false
	}

	address, ok := cascadeTargetAddress(link.TargetPanelId)
	if !ok {
		logger.Warning("cascade outbound: panel ", link.TargetPanelId, " has no address to dial")
		return nil, false
	}

	client, ok := cascadeClient(target, link.TargetClientEmail)
	if !ok {
		logger.Warning("cascade outbound: link ", link.Id, " has no usable client on the target inbound")
		return nil, false
	}

	// The builders read the address off the inbound, which is how a
	// subscription points one at its own host; a copy keeps the stored row out
	// of it.
	dial := *target
	dial.Listen = address

	stream := xrayout.Stream(dial.StreamSettings, xrayout.ClientKey(client), "")
	if h := cascadeHost(target.Id); h != nil {
		applyHostToDial(&dial, stream, h)
	}
	streamSettings, err := json.MarshalIndent(stream, "", "  ")
	if err != nil {
		return nil, false
	}
	built, ok := xrayout.Build(&dial, client, streamSettings, "", link.GeneratedOutboundTag())
	if !ok {
		logger.Warning("cascade outbound: protocol ", dial.Protocol, " cannot be dialed by link ", link.Id)
		return nil, false
	}
	return built, true
}

// cascadeTargetAddress is where the target panel answers. Its registry row
// carries one address whether it is a node or a master.
func cascadeTargetAddress(panelId int) (string, bool) {
	node := &model.Node{}
	q := database.GetDB().Where("id = ?", panelId)
	if panelId == SelfPanelId {
		// A node dialing back into this master uses the address the cluster
		// registered for it.
		q = database.GetDB().Where("is_self = ?", true)
	}
	if err := q.First(node).Error; err != nil {
		return "", false
	}
	if node.Address == "" {
		return "", false
	}
	return node.Address, true
}

// cascadeClient picks the credential the outbound dials with: the one the link
// names, else the first enabled client on the target inbound.
func cascadeClient(target *model.Inbound, email string) (model.Client, bool) {
	clients, err := (&InboundService{}).clientService.ListForInbound(nil, target.Id)
	if err != nil {
		return model.Client{}, false
	}
	for i := range clients {
		client := clients[i]
		if email != "" && client.Email != email {
			continue
		}
		if !client.Enable {
			continue
		}
		return client, true
	}
	return model.Client{}, false
}

// injectCascadeOutbounds adds the generated outbound for every enabled link
// of panelId that did not name one, so the routing pass can target it by tag.
func injectCascadeOutbounds(cfg *xray.Config, links []*model.CascadeLink, panelId int) {
	var generated []any
	for _, link := range links {
		if !link.Enable || link.SourcePanelId != panelId || link.OutboundTag != "" {
			continue
		}
		built, ok := buildCascadeOutbound(link)
		if !ok {
			continue
		}
		var entry any
		if err := json.Unmarshal(built, &entry); err != nil {
			continue
		}
		generated = append(generated, entry)
	}
	if len(generated) == 0 {
		return
	}
	mergeSubscriptionOutbounds(cfg, nil, generated)
}

// cascadeHost is the target inbound's first enabled host, the way users reach
// it: an inbound behind the shared port listens on loopback and is only
// reachable at the address, port and TLS name its host group advertises.
func cascadeHost(inboundId int) *model.Host {
	h := &model.Host{}
	if err := database.GetDB().Where("inbound_id = ? AND is_disabled = ?", inboundId, false).
		Order("sort_order asc, id asc").First(h).Error; err != nil {
		return nil
	}
	return h
}

// applyHostToDial overrides the dial target with a host, as a subscription
// link built from that host would.
func applyHostToDial(dial *model.Inbound, stream map[string]any, h *model.Host) {
	if h.Address != "" {
		dial.Listen = h.Address
	}
	if h.Port > 0 {
		dial.Port = h.Port
	}
	switch h.Security {
	case "tls":
		tls := map[string]any{"serverName": h.Sni}
		if h.Sni == "" {
			tls["serverName"] = dial.Listen
		}
		if len(h.Alpn) > 0 {
			tls["alpn"] = h.Alpn
		}
		if h.Fingerprint != "" {
			tls["fingerprint"] = h.Fingerprint
		}
		if h.AllowInsecure {
			tls["allowInsecure"] = true
		}
		stream["security"] = "tls"
		stream["tlsSettings"] = tls
		delete(stream, "realitySettings")
	case "none":
		stream["security"] = "none"
		delete(stream, "tlsSettings")
		delete(stream, "realitySettings")
	default:
		if tls, ok := stream["tlsSettings"].(map[string]any); ok && h.Sni != "" {
			tls["serverName"] = h.Sni
		}
	}
	network, _ := stream["network"].(string)
	if network == "" {
		return
	}
	settings, _ := stream[network+"Settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
	}
	if h.HostHeader != "" {
		settings["host"] = h.HostHeader
	}
	if h.Path != "" {
		settings["path"] = h.Path
	}
	stream[network+"Settings"] = settings
}
