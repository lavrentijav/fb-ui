package sub

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
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
	observatory      subBalancerObservatoryConfig

	// bakedRouting is re-resolved per request: a remote URL may be cold at
	// construction time and warm up later via the cron job.
	routingRules   string
	bakedRoutingMu sync.Mutex
	bakedRouting   *bakedRoutingState

	// dnsBlock is the panel DNS override, fixed for the service's lifetime.
	dnsBlock map[string]any

	SubService *SubService
}

type bakedRoutingState struct {
	spec       jsonRoutingSpec
	configJson map[string]any
}

// NewSubJsonService creates a new JSON subscription service with the given configuration.
func NewSubJsonService(mux string, rules string, finalMask string, routingRules string, subService *SubService) *SubJsonService {
	var configJson map[string]any
	var defaultOutbounds []json_util.RawMessage
	_ = json.Unmarshal([]byte(defaultJson), &configJson)
	if outboundSlices, ok := configJson["outbounds"].([]any); ok {
		for _, defaultOutbound := range outboundSlices {
			jsonBytes, _ := json.Marshal(defaultOutbound)
			defaultOutbounds = append(defaultOutbounds, jsonBytes)
		}
	}

	// A baked routing profile replaces the template's dns and routing subtrees
	// outright; the legacy simple-rules setting only applies without a profile.
	if routingRules == "" && rules != "" {
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
		routingRules:     routingRules,
		observatory:      defaultSubBalancerObservatoryConfig(),
		SubService:       subService,
	}
}

// Re-resolved per call so an upstream edit reaches the documents without a
// restart; a failed resolve keeps the last good template.
func (s *SubJsonService) bakedTemplate() map[string]any {
	if s.routingRules == "" && s.dnsBlock == nil {
		return s.configJson
	}
	spec := resolveJsonRoutingSpec(s.routingRules)
	s.bakedRoutingMu.Lock()
	defer s.bakedRoutingMu.Unlock()
	if s.bakedRouting != nil {
		if spec.empty() || spec.equal(s.bakedRouting.spec) {
			return s.bakedRouting.configJson
		}
	} else if spec.empty() && s.dnsBlock == nil {
		return s.configJson
	}
	template := make(map[string]any, len(s.configJson)+2)
	maps.Copy(template, s.configJson)
	if !spec.empty() {
		applyJsonRouting(template, spec)
	}
	// The panel-level DNS block is an explicit choice, so it also replaces the
	// dns subtree a routing profile would otherwise bake in.
	if s.dnsBlock != nil {
		template["dns"] = s.dnsBlock
	}
	s.bakedRouting = &bakedRoutingState{spec: spec, configJson: template}
	return template
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
	var hasInactiveExternal bool
	var hasEnabledClient bool

	seenEmails := make(map[string]struct{})
	entries := make([]subConfigEntry, 0, len(inbounds))
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

		var inboundConfigs []json_util.RawMessage
		for _, client := range clients {
			if client.Enable {
				hasEnabledClient = true
			}
			seenEmails[client.Email] = struct{}{}
			inboundConfigs = append(inboundConfigs, s.getConfig(subReq, inbound, client, host)...)
		}
		if len(inboundConfigs) > 0 {
			entries = append(entries, subConfigEntry{
				sortIndex: inbound.SubSortIndex,
				id:        inbound.Id,
				configs:   inboundConfigs,
			})
		}
	}
	entries = s.appendBalancerEntries(entries)

	// Inbounds arrive sorted by (sub_sort_index, id); balancers interleave by
	// the same key and, on an equal number, follow the inbound group.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].sortIndex != entries[j].sortIndex {
			return entries[i].sortIndex < entries[j].sortIndex
		}
		if entries[i].kind != entries[j].kind {
			return entries[i].kind < entries[j].kind
		}
		return entries[i].id < entries[j].id
	})
	var configArray []json_util.RawMessage
	for _, entry := range entries {
		configArray = append(configArray, entry.configs...)
	}
	configArray = dedupeJSONConfigs(configArray)
	for _, ext := range externalLinks {
		if ext.Enable {
			hasEnabledClient = true
		}
		if !ext.Active {
			seenEmails[ext.Email] = struct{}{}
			hasInactiveExternal = true
			continue
		}
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
			maps.Copy(newConfigJson, s.bakedTemplate())
			newConfigJson["outbounds"] = newOutbounds
			newConfigJson["remarks"] = remark
			newConfig, _ := json.MarshalIndent(newConfigJson, "", "  ")
			configArray = append(configArray, newConfig)
		}
	}

	if len(configArray) == 0 && !hasInactiveExternal {
		return "", "", nil
	}

	emails := make([]string, 0, len(seenEmails))
	for e := range seenEmails {
		emails = append(emails, e)
	}
	slices.Sort(emails)
	traffic, _ := subReq.AggregateTrafficByEmails(emails)
	traffic.Enable = hasEnabledClient
	header = subReq.subscriptionUserinfo(traffic)

	if mode, remark := subReq.resolveInfoNodeRemark(subId, emails, traffic, len(configArray) > 0); mode != infoNodeNone {
		dummyConfig := s.genDummySocksConfig(remark)
		if mode == infoNodeExpired || mode == infoNodeDepleted {
			configArray = []json_util.RawMessage{dummyConfig}
		} else {
			configArray = append([]json_util.RawMessage{dummyConfig}, configArray...)
		}
	}

	if len(configArray) == 0 {
		return "", header, nil
	}

	var finalJson []byte
	if len(configArray) == 1 && !alwaysReturnArray {
		finalJson, _ = json.MarshalIndent(configArray[0], "", "  ")
	} else {
		finalJson, _ = json.MarshalIndent(configArray, "", "  ")
	}

	return string(finalJson), header, nil
}

// subConfigEntry is one ordered block of the JSON subscription: an inbound's
// configs (kind 0) or a balancer config (kind 1).
type subConfigEntry struct {
	sortIndex int
	kind      int
	id        int
	configs   []json_util.RawMessage
}

const (
	subBalancerTag      = "balancer"
	subBalancerProbeURL = "https://www.google.com/generate_204"
)

// subBalancerObservatoryConfig is the panel-wide burstObservatory ping config
// emitted into every client-side balancer doc (subJsonObservatory setting).
type subBalancerObservatoryConfig struct {
	Destination  string `json:"destination"`
	Connectivity string `json:"connectivity"`
	Interval     string `json:"interval"`
	Sampling     int    `json:"sampling"`
	Timeout      string `json:"timeout"`
	HTTPMethod   string `json:"httpMethod"`
}

func defaultSubBalancerObservatoryConfig() subBalancerObservatoryConfig {
	return subBalancerObservatoryConfig{
		Destination:  subBalancerProbeURL,
		Connectivity: "",
		Interval:     "1m",
		Sampling:     2,
		Timeout:      "5s",
		HTTPMethod:   "HEAD",
	}
}

// SetObservatoryConfig overrides defaults from the panel JSON setting. An empty
// cfg keeps all defaults; invalid values fall back with a warning, never panic.
func (s *SubJsonService) SetObservatoryConfig(cfg string) {
	s.observatory = defaultSubBalancerObservatoryConfig()
	if cfg == "" {
		return
	}
	var parsed subBalancerObservatoryConfig
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		logger.Warningf("subJsonObservatory: invalid JSON %q, using defaults: %v", cfg, err)
		return
	}
	if parsed.Destination != "" {
		if validProbeURL(parsed.Destination) {
			s.observatory.Destination = parsed.Destination
		} else {
			logger.Warningf("subJsonObservatory: invalid destination %q, keeping default %q", parsed.Destination, s.observatory.Destination)
		}
	}
	if parsed.Connectivity != "" {
		if validProbeURL(parsed.Connectivity) {
			s.observatory.Connectivity = parsed.Connectivity
		} else {
			logger.Warningf("subJsonObservatory: invalid connectivity %q, keeping default (skip)", parsed.Connectivity)
		}
	}
	if parsed.Interval != "" {
		if _, err := time.ParseDuration(parsed.Interval); err == nil {
			s.observatory.Interval = parsed.Interval
		} else {
			logger.Warningf("subJsonObservatory: invalid interval %q, keeping default %q", parsed.Interval, s.observatory.Interval)
		}
	}
	if parsed.Sampling > 0 {
		s.observatory.Sampling = parsed.Sampling
	}
	if parsed.Timeout != "" {
		if _, err := time.ParseDuration(parsed.Timeout); err == nil {
			s.observatory.Timeout = parsed.Timeout
		} else {
			logger.Warningf("subJsonObservatory: invalid timeout %q, keeping default %q", parsed.Timeout, s.observatory.Timeout)
		}
	}
	if parsed.HTTPMethod == "HEAD" || parsed.HTTPMethod == "GET" {
		s.observatory.HTTPMethod = parsed.HTTPMethod
	}
}

// validProbeURL accepts only absolute http(s) URLs so a malformed probe or
// connectivity value can't slip into the emitted burstObservatory.
func validProbeURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u == nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func (s *SubJsonService) balancerObservatory(prefix string) map[string]any {
	o := s.observatory
	return map[string]any{
		"subjectSelector": []string{prefix},
		"pingConfig": map[string]any{
			"destination":  o.Destination,
			"connectivity": o.Connectivity,
			"interval":     o.Interval,
			"sampling":     o.Sampling,
			"timeout":      o.Timeout,
			"httpMethod":   o.HTTPMethod,
		},
	}
}

// appendBalancerEntries appends one entry per enabled balancer that has at
// least one member outbound among the inbound entries.
func (s *SubJsonService) appendBalancerEntries(entries []subConfigEntry) []subConfigEntry {
	balancers := getEnabledSubBalancers()
	if len(balancers) == 0 {
		return entries
	}
	// Pre-pass: pull each inbound doc's proxy outbound once so every balancer
	// reuses it instead of re-unmarshalling the whole document per balancer.
	entryProxies := make([][]map[string]any, len(entries))
	for i, entry := range entries {
		if entry.kind != 0 {
			continue
		}
		for _, config := range entry.configs {
			if proxy := extractProxyOutbound(config); proxy != nil {
				entryProxies[i] = append(entryProxies[i], proxy)
			}
		}
	}
	for i := range balancers {
		config := s.buildBalancerConfig(&balancers[i], entries, entryProxies)
		if config == nil {
			continue
		}
		entries = append(entries, subConfigEntry{
			sortIndex: balancers[i].SortOrder,
			kind:      1,
			id:        balancers[i].Id,
			configs:   []json_util.RawMessage{config},
		})
	}
	return entries
}

// extractProxyOutbound returns the first outbound of a document when it is the
// proxy (tag == "proxy"), else nil — the only member shape a balancer retags.
func extractProxyOutbound(config json_util.RawMessage) map[string]any {
	var doc map[string]any
	if json.Unmarshal(config, &doc) != nil {
		return nil
	}
	outbounds, _ := doc["outbounds"].([]any)
	if len(outbounds) == 0 {
		return nil
	}
	outbound, _ := outbounds[0].(map[string]any)
	if outbound == nil || outbound["tag"] != "proxy" {
		return nil
	}
	return outbound
}

func getEnabledSubBalancers() []model.SubBalancer {
	var balancers []model.SubBalancer
	if err := database.GetDB().Model(&model.SubBalancer{}).
		Where("enabled = ?", true).
		Order("sort_order asc, id asc").Find(&balancers).Error; err != nil {
		logger.Error("SubJsonService - getEnabledSubBalancers:", err)
		return nil
	}
	return balancers
}

// Suffix by proxy protocol, not transport network — a vmess/tcp member used to
// be mislabelled "vless".
func balancerMemberSuffix(protocol string) string {
	if protocol == "" {
		return "other"
	}
	return protocol
}

// balMember is one retagged member outbound and the inbound it came from.
type balMember struct {
	tag       string
	inboundId int
}

// leastLoadCosts builds xray's static strategy costs: higher value = picked
// less often; nil unless a member carries an explicit weight (all-1.0 bloat).
func leastLoadCosts(balancer *model.SubBalancer, members []balMember) []any {
	if balancer.Strategy != "leastLoad" || len(members) == 0 || len(balancer.MemberWeights) == 0 {
		return nil
	}
	costs := make([]any, 0, len(members))
	configured := false
	for _, m := range members {
		value := 1.0
		if weight, ok := balancer.MemberWeights[m.inboundId]; ok && weight > 0 {
			value = weight
			configured = true
		}
		// Anchored regexp: plain cost matching is substring-based in xray, so
		// an unanchored "bal-1-vless" would also swallow "bal-1-vless-2".
		costs = append(costs, map[string]any{
			"regexp": true,
			"match":  "^" + m.tag + "$",
			"value":  value,
		})
	}
	if !configured {
		return nil
	}
	return costs
}

// buildBalancerConfig assembles the balancer profile: members retagged under a
// per-balancer prefix, a routing.balancers entry, and (for leastPing/leastLoad) an observatory.
func (s *SubJsonService) buildBalancerConfig(balancer *model.SubBalancer, entries []subConfigEntry, entryProxies [][]map[string]any) json_util.RawMessage {
	prefix := fmt.Sprintf("bal-%d-", balancer.Id)
	usedTags := make(map[string]bool)
	var proxies []json_util.RawMessage
	// Members in emission order with their owning inbound, so costs[] can
	// reference the exact retagged tags assigned here.
	var members []balMember
	var firstTag string
	// entryProxies is the pre-extracted proxy outbounds per entry; kind!=0 rows
	// have none. Clone before retagging so the cached map stays reusable.
	for i, entry := range entries {
		if entry.kind != 0 || !slices.Contains(balancer.InboundIds, entry.id) {
			continue
		}
		for _, outbound := range entryProxies[i] {
			protocol, _ := outbound["protocol"].(string)
			base := prefix + balancerMemberSuffix(protocol)
			tag := base
			for suffix := 2; usedTags[tag]; suffix++ {
				tag = fmt.Sprintf("%s-%d", base, suffix)
			}
			usedTags[tag] = true
			member := maps.Clone(outbound)
			member["tag"] = tag
			if raw, err := json.MarshalIndent(member, "", "  "); err == nil {
				members = append(members, balMember{tag: tag, inboundId: entry.id})
				if firstTag == "" {
					firstTag = tag
				}
				proxies = append(proxies, raw)
			}
		}
	}
	if len(proxies) == 0 {
		return nil
	}

	outbounds := append([]json_util.RawMessage{}, proxies...)
	outbounds = append(outbounds, s.defaultOutbounds...)

	// One template per document: two resolves could straddle a profile refresh
	// and pair this document's dns with the other revision's routing.
	template := s.bakedTemplate()
	// Clone the shared routing subtree (and each rule map) before pointing
	// rules at the balancer.
	baseRouting, _ := template["routing"].(map[string]any)
	routing := make(map[string]any, len(baseRouting)+1)
	maps.Copy(routing, baseRouting)
	baseRules, _ := baseRouting["rules"].([]any)
	rules := make([]any, 0, len(baseRules)+1)
	for _, rule := range baseRules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			rules = append(rules, rule)
			continue
		}
		ruleMap = maps.Clone(ruleMap)
		if ruleMap["outboundTag"] == "proxy" {
			delete(ruleMap, "outboundTag")
			ruleMap["balancerTag"] = subBalancerTag
		}
		rules = append(rules, ruleMap)
	}
	routing["rules"] = rules
	isObservatory := balancer.Strategy == "leastPing" || balancer.Strategy == "leastLoad"
	strategyEntry := map[string]any{"type": balancer.Strategy}
	if costs := leastLoadCosts(balancer, members); costs != nil {
		strategyEntry["settings"] = map[string]any{"costs": costs}
	}
	balancerEntry := map[string]any{
		"tag":      subBalancerTag,
		"selector": []string{prefix},
		"strategy": strategyEntry,
	}
	if isObservatory && firstTag != "" {
		// With all probes failing, route to the first member instead of
		// failing dispatch.
		balancerEntry["fallbackTag"] = firstTag
	}
	routing["balancers"] = []any{balancerEntry}

	newConfigJson := make(map[string]any, len(template)+2)
	maps.Copy(newConfigJson, template)
	newConfigJson["outbounds"] = outbounds
	newConfigJson["remarks"] = balancer.Remark
	newConfigJson["routing"] = routing
	// leastPing/leastLoad require a burst observatory (Xray refuses to start
	// them without one); fallbackTag above covers the probe-outage case.
	if isObservatory {
		newConfigJson["burstObservatory"] = s.balancerObservatory(prefix)
	}

	config, _ := json.MarshalIndent(newConfigJson, "", "  ")
	return config
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
		case "amneziawg", "tuic":
			continue
		}

		newOutbounds = append(newOutbounds, s.defaultOutbounds...)
		newConfigJson := make(map[string]any)
		maps.Copy(newConfigJson, s.bakedTemplate())

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
	if ka := client.KeepAliveSeconds(); ka > 0 {
		peer["keepAlive"] = ka
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

func (s *SubJsonService) genDummySocksConfig(remark string) json_util.RawMessage {
	outbound := map[string]any{
		"protocol": "socks",
		"tag":      "proxy",
		"settings": map[string]any{
			"servers": []any{
				map[string]any{
					"address": "127.0.0.1",
					"port":    1080,
				},
			},
		},
	}
	rawOutbound, _ := json.Marshal(outbound)
	newOutbounds := []json_util.RawMessage{rawOutbound}
	newOutbounds = append(newOutbounds, s.defaultOutbounds...)

	newConfigJson := make(map[string]any)
	maps.Copy(newConfigJson, s.configJson)
	if s.dnsBlock != nil {
		newConfigJson["dns"] = s.dnsBlock
	}
	newConfigJson["outbounds"] = newOutbounds
	newConfigJson["remarks"] = remark

	newConfig, _ := json.MarshalIndent(newConfigJson, "", "  ")
	return newConfig
}
