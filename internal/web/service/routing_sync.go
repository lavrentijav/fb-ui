package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ManagedRouting is the share of the cluster routing a master owns for one
// node: the cascade outbounds that node dials and the filter and cascade rules
// for its inbounds. The node splices it into the config it generates.
type ManagedRouting struct {
	Version   string            `json:"version" example:"3f2a…"`
	Outbounds []json.RawMessage `json:"outbounds"`
	Rules     []json.RawMessage `json:"rules"`
}

const managedRoutingKey = "managedRouting"

// syntheticTemplate stands in for the node's own config: it only has to carry
// the outbound tags a filter action may target, which the stock template
// always has on every node.
func syntheticTemplate() *xray.Config {
	return &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`),
		RouterConfig:    json_util.RawMessage(`{}`),
	}
}

// BuildManagedRouting renders node nodeId's share; ruleIds and linkIds are
// what made it in, for the applied marks once the node confirms.
func BuildManagedRouting(nodeId int) (mr *ManagedRouting, ruleIds, linkIds []int, err error) {
	filters := FilterService{}
	rules, err := filters.Rules()
	if err != nil {
		return nil, nil, nil, err
	}
	lists, err := filters.Lists()
	if err != nil {
		return nil, nil, nil, err
	}
	var links []*model.CascadeLink
	if err := database.GetDB().Order("id asc").Find(&links).Error; err != nil {
		return nil, nil, nil, err
	}
	var tags []string
	if err := database.GetDB().Model(&model.Inbound{}).Where("node_id = ? AND enable = ?", nodeId, true).
		Order("id asc").Pluck("tag", &tags).Error; err != nil {
		return nil, nil, nil, err
	}
	cfg := syntheticTemplate()
	injectCascadeOutbounds(cfg, links, nodeId)
	ruleIds, linkIds = injectClusterRouting(cfg, rules, lists, links, tags, nodeId)

	mr = &ManagedRouting{Outbounds: []json.RawMessage{}, Rules: []json.RawMessage{}}
	var outbounds []json.RawMessage
	_ = json.Unmarshal(cfg.OutboundConfigs, &outbounds)
	for _, ob := range outbounds {
		var head struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(ob, &head) == nil && head.Tag != "direct" && head.Tag != "blocked" {
			mr.Outbounds = append(mr.Outbounds, ob)
		}
	}
	var routing struct {
		Rules []json.RawMessage `json:"rules"`
	}
	_ = json.Unmarshal(cfg.RouterConfig, &routing)
	if routing.Rules != nil {
		mr.Rules = routing.Rules
	}
	sum := sha256.New()
	for _, part := range [][]json.RawMessage{mr.Outbounds, mr.Rules} {
		for _, raw := range part {
			sum.Write(raw)
			sum.Write([]byte{0})
		}
		sum.Write([]byte{1})
	}
	mr.Version = hex.EncodeToString(sum.Sum(nil))
	return mr, ruleIds, linkIds, nil
}

// StoreManagedRouting is the node side: keep what the master sent and have
// Xray regenerate when it changed.
func (s *XrayService) StoreManagedRouting(mr *ManagedRouting) (changed bool, err error) {
	if mr == nil || mr.Version == "" {
		return false, errors.New("managed routing without a version")
	}
	var settings SettingService
	prev, _ := settings.getString(managedRoutingKey)
	var old ManagedRouting
	if prev != "" && json.Unmarshal([]byte(prev), &old) == nil && old.Version == mr.Version {
		return false, nil
	}
	b, err := json.Marshal(mr)
	if err != nil {
		return false, err
	}
	if err := settings.saveSetting(managedRoutingKey, string(b)); err != nil {
		return false, err
	}
	s.SetToNeedRestart()
	return true, nil
}

// CurrentManagedRouting is what this panel holds from its master, if anything.
func CurrentManagedRouting() (*ManagedRouting, error) {
	var settings SettingService
	raw, err := settings.getString(managedRoutingKey)
	if err != nil || raw == "" {
		return &ManagedRouting{}, nil
	}
	mr := &ManagedRouting{}
	return mr, json.Unmarshal([]byte(raw), mr)
}

// injectManagedRouting splices the master's share into this node's config:
// outbounds a tag at a time (the node's own win) and rules ahead of the
// template's, dropping a rule whose target this config does not have.
func injectManagedRouting(cfg *xray.Config) {
	var settings SettingService
	raw, _ := settings.getString(managedRoutingKey)
	if raw == "" {
		return
	}
	var mr ManagedRouting
	if err := json.Unmarshal([]byte(raw), &mr); err != nil {
		logger.Warning("managed routing: stored value is unreadable:", err)
		return
	}
	var outbounds []any
	_ = json.Unmarshal(cfg.OutboundConfigs, &outbounds)
	have := map[string]bool{}
	for _, ob := range outbounds {
		if m, ok := ob.(map[string]any); ok {
			if tag, _ := m["tag"].(string); tag != "" {
				have[tag] = true
			}
		}
	}
	for _, ob := range mr.Outbounds {
		var m map[string]any
		if json.Unmarshal(ob, &m) != nil {
			continue
		}
		if tag, _ := m["tag"].(string); tag != "" && !have[tag] {
			outbounds = append(outbounds, m)
			have[tag] = true
		}
	}
	if b, err := json.Marshal(outbounds); err == nil {
		cfg.OutboundConfigs = json_util.RawMessage(b)
	}
	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 && json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return
	}
	var add []any
	for _, r := range mr.Rules {
		var m map[string]any
		if json.Unmarshal(r, &m) != nil {
			continue
		}
		target, _ := m["outboundTag"].(string)
		if b, _ := m["balancerTag"].(string); b != "" {
			target = b
		}
		if target != "" && !have[target] && !routingTagIsBalancer(routing, target) {
			logger.Warning("managed routing: no outbound", target, "here, rule skipped")
			continue
		}
		add = append(add, m)
	}
	if len(add) == 0 {
		return
	}
	existing, _ := routing["rules"].([]any)
	routing["rules"] = append(add, existing...)
	if b, err := json.Marshal(routing); err == nil {
		cfg.RouterConfig = json_util.RawMessage(b)
	}
}

// managedRoutingPusher delivers a share to a node; tests replace it.
var managedRoutingPusher = func(ctx context.Context, n *model.Node, mr *ManagedRouting) error {
	mgr := runtime.GetManager()
	if mgr == nil {
		return errors.New("runtime manager unavailable")
	}
	remote, err := mgr.RemoteFor(n)
	if err != nil {
		return err
	}
	return remote.ApplyManagedRouting(ctx, mr)
}

var (
	routingPushedMu sync.Mutex
	routingPushed   = map[int]string{}
	routingFailedAt = map[int]time.Time{}
)

// routingRetryAfter keeps an old node without the endpoint from being asked
// again on every tick.
const routingRetryAfter = 5 * time.Minute

// SyncManagedRouting pushes every online node the share it does not have
// yet. The last version each node accepted is remembered in memory, so a
// restart re-sends once and the node ignores a version it already holds.
func SyncManagedRouting() {
	var nodes []*model.Node
	if err := database.GetDB().Where("enable = ? AND role = ? AND status = ?", true, model.NodeRoleNode, "online").
		Find(&nodes).Error; err != nil {
		logger.Warning("managed routing: list nodes:", err)
		return
	}
	for _, n := range nodes {
		mr, ruleIds, linkIds, err := BuildManagedRouting(n.Id)
		if err != nil {
			logger.Warning("managed routing: build for node", n.Id, err)
			continue
		}
		routingPushedMu.Lock()
		skip := routingPushed[n.Id] == mr.Version || time.Since(routingFailedAt[n.Id]) < routingRetryAfter
		routingPushedMu.Unlock()
		if skip {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = managedRoutingPusher(ctx, n, mr)
		cancel()
		routingPushedMu.Lock()
		if err != nil {
			routingFailedAt[n.Id] = time.Now()
		} else {
			routingPushed[n.Id] = mr.Version
			delete(routingFailedAt, n.Id)
		}
		routingPushedMu.Unlock()
		if err != nil {
			logger.Warning("managed routing: push to node", n.Name, err)
			continue
		}
		if err := markPanelApplied(n.Id, ruleIds, linkIds); err != nil {
			logger.Warning("managed routing: record applied rules for node", n.Id, err)
		}
	}
}

// markPanelApplied records which of a node's rules and links reached it.
func markPanelApplied(nodeId int, ruleIds, linkIds []int) error {
	db := database.GetDB()
	now := time.Now().Unix()
	for _, t := range []struct {
		table any
		owner string
		ids   []int
	}{{&model.FilterRule{}, "panel_id = ?", ruleIds}, {&model.CascadeLink{}, "source_panel_id = ?", linkIds}} {
		pending := db.Model(t.table).Where(t.owner, nodeId)
		if len(t.ids) > 0 {
			pending = pending.Where("id NOT IN ?", t.ids)
		}
		if err := pending.Update("applied", 0).Error; err != nil {
			return err
		}
		if len(t.ids) > 0 {
			if err := db.Model(t.table).Where("id IN ?", t.ids).Update("applied", now).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
