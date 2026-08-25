package service

import (
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Outbound tags a filter action resolves to in the generated config. They are
// the tags the stock template ships; a template without them cannot express
// the action, and the rule is skipped rather than guessed at.
var (
	blockOutboundTags  = []string{"blocked", "block"}
	directOutboundTags = []string{"direct", "freedom"}
)

// injectClusterRouting materializes this panel's filter chain into the routing
// section of the generated config, in the order the layers are stored — Xray
// takes the first matching rule, so that order is the chain. Returns the ids of
// the rules that actually reached the config.
//
// Rules owned by another panel belong in that panel's own config and are left
// to it; the same goes for a cascade whose outbound does not exist here yet.
func injectClusterRouting(
	cfg *xray.Config,
	rules []*model.FilterRule,
	lists []*model.FilterList,
	links []*model.CascadeLink,
	localInboundTags []string,
) (appliedRules []int, appliedLinks []int) {
	if len(rules) == 0 && len(links) == 0 {
		return nil, nil
	}
	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("cluster routing: routing section is unparsable, skipping injection:", err)
			return nil, nil
		}
	}

	listById := make(map[int]*model.FilterList, len(lists))
	for _, list := range lists {
		listById[list.Id] = list
	}
	linkById := make(map[int]*model.CascadeLink, len(links))
	for _, link := range links {
		linkById[link.Id] = link
	}

	newRules := make([]any, 0, len(rules)+len(links))
	applied := make([]int, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enable || rule.PanelId != SelfPanelId {
			continue
		}
		domains, ips := ruleMatchers(rule, listById)
		if len(domains) == 0 && len(ips) == 0 {
			// A rule with no matchers matches everything: blocking on it would
			// black-hole the whole panel, so it never reaches the config.
			logger.Warning("cluster routing: filter rule [", rule.Name, "] has no usable entries, skipping")
			continue
		}
		target, ok := ruleOutboundTag(cfg, routing, rule, linkById)
		if !ok {
			continue
		}

		tags := rule.SourceInboundTags
		if len(tags) == 0 {
			tags = localInboundTags
		}
		if len(tags) == 0 {
			logger.Warning("cluster routing: filter rule [", rule.Name, "] has no inbounds to watch, skipping")
			continue
		}

		entry := map[string]any{
			"type":       "field",
			"inboundTag": toAnySlice(tags),
		}
		if len(domains) > 0 {
			entry["domain"] = toAnySlice(domains)
		}
		if len(ips) > 0 {
			entry["ip"] = toAnySlice(ips)
		}
		if routingTagIsBalancer(routing, target) {
			entry["balancerTag"] = target
		} else {
			entry["outboundTag"] = target
		}
		newRules = append(newRules, entry)
		applied = append(applied, rule.Id)
	}

	// The links come after the layers: a layer decides about part of an
	// inbound's traffic, the link is what happens to the rest of it.
	appliedLinks = make([]int, 0, len(links))
	for _, link := range links {
		if !link.Enable || link.SourcePanelId != SelfPanelId || link.SourceInboundTag == "" {
			continue
		}
		tag := link.EffectiveOutboundTag()
		if !routingTargetExists(routing, cfg.OutboundConfigs, tag) {
			logger.Warning("cluster routing: outbound [", tag, "] for cascade link ", link.Id, " does not exist, link stays pending")
			continue
		}
		entry := map[string]any{
			"type":       "field",
			"inboundTag": []any{link.SourceInboundTag},
		}
		if routingTagIsBalancer(routing, tag) {
			entry["balancerTag"] = tag
		} else {
			entry["outboundTag"] = tag
		}
		newRules = append(newRules, entry)
		appliedLinks = append(appliedLinks, link.Id)
	}

	if len(newRules) == 0 {
		return nil, nil
	}
	existing, _ := routing["rules"].([]any)
	routing["rules"] = append(newRules, existing...)
	encoded, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("cluster routing: failed to rebuild routing section, skipping injection:", err)
		return nil, nil
	}
	cfg.RouterConfig = json_util.RawMessage(encoded)
	return applied, appliedLinks
}

// ruleMatchers splits the entries of a rule's lists into the two matcher arrays
// an Xray field rule takes. A disabled list contributes nothing.
func ruleMatchers(rule *model.FilterRule, lists map[int]*model.FilterList) ([]string, []string) {
	var domains, ips []string
	for _, listId := range rule.ListIds {
		list, ok := lists[listId]
		if !ok || !list.Enable {
			continue
		}
		if list.Kind == model.FilterKindIP {
			ips = append(ips, list.Entries...)
			continue
		}
		domains = append(domains, list.Entries...)
	}
	return domains, ips
}

func ruleOutboundTag(
	cfg *xray.Config,
	routing map[string]any,
	rule *model.FilterRule,
	links map[int]*model.CascadeLink,
) (string, bool) {
	switch rule.Action {
	case model.FilterActionCascade:
		link, ok := links[rule.CascadeLinkId]
		if !ok {
			logger.Warning("cluster routing: filter rule [", rule.Name, "] names a cascade link that is gone, skipping")
			return "", false
		}
		if !link.Enable {
			return "", false
		}
		tag := link.EffectiveOutboundTag()
		if !routingTargetExists(routing, cfg.OutboundConfigs, tag) {
			// The cascade outbound is not generated yet; the rule stays pending
			// instead of silently routing traffic somewhere else.
			logger.Warning("cluster routing: cascade outbound [", tag, "] does not exist yet, rule [", rule.Name, "] stays pending")
			return "", false
		}
		return tag, true
	case model.FilterActionDirect:
		return firstExistingTag(cfg, routing, directOutboundTags, rule.Name)
	default:
		return firstExistingTag(cfg, routing, blockOutboundTags, rule.Name)
	}
}

func firstExistingTag(cfg *xray.Config, routing map[string]any, candidates []string, ruleName string) (string, bool) {
	for _, tag := range candidates {
		if routingTargetExists(routing, cfg.OutboundConfigs, tag) {
			return tag, true
		}
	}
	logger.Warning("cluster routing: no outbound tagged ", candidates[0], " in this config, rule [", ruleName, "] stays pending")
	return "", false
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// markClusterApplied records which of this panel's filter rules are in the
// config that just started. Anything that did not make it goes back to pending
// rather than keeping a stale "applied" from an earlier config.
func markClusterApplied(ruleIds, linkIds []int) error {
	if err := markApplied(&model.FilterRule{}, "panel_id = ?", ruleIds); err != nil {
		return err
	}
	return markApplied(&model.CascadeLink{}, "source_panel_id = ?", linkIds)
}

func markApplied(table any, ownerColumn string, appliedIds []int) error {
	db := database.GetDB()
	pending := db.Model(table).Where(ownerColumn, SelfPanelId)
	if len(appliedIds) > 0 {
		pending = pending.Where("id NOT IN ?", appliedIds)
	}
	if err := pending.Update("applied", 0).Error; err != nil {
		return err
	}
	if len(appliedIds) == 0 {
		return nil
	}
	return db.Model(table).Where("id IN ?", appliedIds).
		Update("applied", time.Now().Unix()).Error
}
