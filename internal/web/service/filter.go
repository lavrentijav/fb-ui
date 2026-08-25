package service

import (
	"net"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// maxFilterEntries bounds one list. Xray copes with far more, but a runaway
// paste is worth catching at the API rather than in the routing engine.
const maxFilterEntries = 20000

type FilterService struct{}

func (s *FilterService) Lists() ([]*model.FilterList, error) {
	var lists []*model.FilterList
	if err := database.GetDB().Order("id asc").Find(&lists).Error; err != nil {
		return nil, err
	}
	return lists, nil
}

func (s *FilterService) ListById(id int) (*model.FilterList, error) {
	list := &model.FilterList{}
	if err := database.GetDB().Where("id = ?", id).First(list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *FilterService) CreateList(list *model.FilterList) error {
	if err := s.normalizeList(list); err != nil {
		return err
	}
	db := database.GetDB()
	wantEnabled := list.Enable
	if err := db.Create(list).Error; err != nil {
		return err
	}
	if wantEnabled {
		return nil
	}
	// enable declares a default, so GORM drops a false value from the INSERT
	// and then reads the row back as enabled.
	if err := db.Model(model.FilterList{}).Where("id = ?", list.Id).Update("enable", false).Error; err != nil {
		return err
	}
	list.Enable = false
	return nil
}

func (s *FilterService) UpdateList(id int, in *model.FilterList) error {
	if err := s.normalizeList(in); err != nil {
		return err
	}
	db := database.GetDB()
	existing := &model.FilterList{}
	if err := db.Where("id = ?", id).First(existing).Error; err != nil {
		return err
	}
	return db.Model(existing).Select("Name", "Remark", "Kind", "Entries", "Enable").Updates(model.FilterList{
		Name: in.Name, Remark: in.Remark, Kind: in.Kind, Entries: in.Entries, Enable: in.Enable,
	}).Error
}

// DeleteList refuses to strand a rule: a list still referenced would leave the
// rule matching nothing, which reads as "the filter stopped working".
func (s *FilterService) DeleteList(id int) error {
	rules, err := s.Rules()
	if err != nil {
		return err
	}
	var used []string
	for _, rule := range rules {
		for _, listId := range rule.ListIds {
			if listId == id {
				used = append(used, rule.Name)
				break
			}
		}
	}
	if len(used) > 0 {
		return common.NewError("list is still used by rule(s): " + strings.Join(used, ", "))
	}
	return database.GetDB().Where("id = ?", id).Delete(&model.FilterList{}).Error
}

func (s *FilterService) normalizeList(list *model.FilterList) error {
	list.Name = strings.TrimSpace(list.Name)
	if list.Name == "" {
		return common.NewError("filter list name is required")
	}
	list.Remark = strings.TrimSpace(list.Remark)
	if list.Kind != model.FilterKindIP {
		list.Kind = model.FilterKindDomain
	}

	seen := make(map[string]struct{}, len(list.Entries))
	entries := make([]string, 0, len(list.Entries))
	for _, raw := range list.Entries {
		entry := strings.TrimSpace(raw)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if _, dup := seen[entry]; dup {
			continue
		}
		if err := validateFilterEntry(list.Kind, entry); err != nil {
			return err
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	if len(entries) > maxFilterEntries {
		return common.NewErrorf("filter list holds %d entries, the limit is %d", len(entries), maxFilterEntries)
	}
	list.Entries = entries
	return nil
}

// validateFilterEntry keeps out values Xray would reject at config load, when
// the failure is a dead proxy rather than a form error.
func validateFilterEntry(kind, entry string) error {
	if strings.ContainsAny(entry, " \t\r\n,") {
		return common.NewError("filter entry " + entry + " must not contain spaces or commas")
	}
	prefix, rest, hasPrefix := strings.Cut(entry, ":")
	if kind == model.FilterKindIP {
		if hasPrefix && prefix == "geoip" {
			if rest == "" {
				return common.NewError("geoip entry needs a category")
			}
			return nil
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			return nil
		}
		if net.ParseIP(entry) != nil {
			return nil
		}
		return common.NewError("filter entry " + entry + " is not an IP, a CIDR or a geoip category")
	}
	if hasPrefix {
		switch prefix {
		case "geosite", "regexp", "domain", "full", "ext":
			if rest == "" {
				return common.NewError("filter entry " + entry + " needs a value after the prefix")
			}
			return nil
		default:
			return common.NewError("filter entry " + entry + " uses an unknown prefix " + prefix)
		}
	}
	if strings.Contains(entry, "/") {
		return common.NewError("filter entry " + entry + " looks like a path, not a domain")
	}
	return nil
}

func (s *FilterService) Rules() ([]*model.FilterRule, error) {
	var rules []*model.FilterRule
	if err := database.GetDB().Order("sort_order asc").Order("id asc").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *FilterService) RuleById(id int) (*model.FilterRule, error) {
	rule := &model.FilterRule{}
	if err := database.GetDB().Where("id = ?", id).First(rule).Error; err != nil {
		return nil, err
	}
	return rule, nil
}

func (s *FilterService) CreateRule(rule *model.FilterRule) error {
	if err := s.normalizeRule(rule); err != nil {
		return err
	}
	rule.Applied = 0
	db := database.GetDB()
	wantEnabled := rule.Enable
	if err := db.Create(rule).Error; err != nil {
		return err
	}
	if !wantEnabled {
		if err := db.Model(model.FilterRule{}).Where("id = ?", rule.Id).Update("enable", false).Error; err != nil {
			return err
		}
		rule.Enable = false
	}
	return (&NetworkService{}).markSourceDirty(rule.PanelId)
}

func (s *FilterService) UpdateRule(id int, in *model.FilterRule) error {
	if err := s.normalizeRule(in); err != nil {
		return err
	}
	db := database.GetDB()
	existing := &model.FilterRule{}
	if err := db.Where("id = ?", id).First(existing).Error; err != nil {
		return err
	}
	if err := db.Model(existing).
		Select("Name", "Remark", "PanelId", "SourceInboundTags", "ListIds", "Action", "CascadeLinkId", "SortOrder", "Enable", "Applied").
		Updates(model.FilterRule{
			Name: in.Name, Remark: in.Remark, PanelId: in.PanelId,
			SourceInboundTags: in.SourceInboundTags, ListIds: in.ListIds,
			Action: in.Action, CascadeLinkId: in.CascadeLinkId,
			SortOrder: in.SortOrder, Enable: in.Enable, Applied: 0,
		}).Error; err != nil {
		return err
	}
	network := &NetworkService{}
	if existing.PanelId != in.PanelId {
		if err := network.markSourceDirty(existing.PanelId); err != nil {
			return err
		}
	}
	return network.markSourceDirty(in.PanelId)
}

func (s *FilterService) DeleteRule(id int) error {
	db := database.GetDB()
	rule := &model.FilterRule{}
	if err := db.Where("id = ?", id).First(rule).Error; err != nil {
		return err
	}
	if err := db.Where("id = ?", id).Delete(&model.FilterRule{}).Error; err != nil {
		return err
	}
	return (&NetworkService{}).markSourceDirty(rule.PanelId)
}

func (s *FilterService) SetRuleEnable(id int, enable bool) error {
	db := database.GetDB()
	rule := &model.FilterRule{}
	if err := db.Where("id = ?", id).First(rule).Error; err != nil {
		return err
	}
	if err := db.Model(model.FilterRule{}).Where("id = ?", id).
		Updates(map[string]any{"enable": enable, "applied": 0}).Error; err != nil {
		return err
	}
	return (&NetworkService{}).markSourceDirty(rule.PanelId)
}

func (s *FilterService) normalizeRule(rule *model.FilterRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		return common.NewError("filter rule name is required")
	}
	rule.Remark = strings.TrimSpace(rule.Remark)
	switch rule.Action {
	case model.FilterActionDirect, model.FilterActionCascade:
	default:
		rule.Action = model.FilterActionBlock
	}

	if err := (&NetworkService{}).panelExists(rule.PanelId); err != nil {
		return err
	}

	tags := make([]string, 0, len(rule.SourceInboundTags))
	seen := make(map[string]struct{}, len(rule.SourceInboundTags))
	for _, raw := range rule.SourceInboundTags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	rule.SourceInboundTags = tags

	if len(rule.ListIds) == 0 {
		return common.NewError("filter rule needs at least one list")
	}
	ids := make([]int, 0, len(rule.ListIds))
	seenIds := make(map[int]struct{}, len(rule.ListIds))
	for _, id := range rule.ListIds {
		if _, dup := seenIds[id]; dup {
			continue
		}
		var count int64
		if err := database.GetDB().Model(model.FilterList{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return common.NewErrorf("filter list %d not found", id)
		}
		seenIds[id] = struct{}{}
		ids = append(ids, id)
	}
	rule.ListIds = ids

	if rule.Action == model.FilterActionCascade {
		if rule.CascadeLinkId == 0 {
			return common.NewError("a cascade rule needs the link to divert onto")
		}
		var count int64
		if err := database.GetDB().Model(model.CascadeLink{}).Where("id = ?", rule.CascadeLinkId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return common.NewError("cascade link not found")
		}
	} else {
		rule.CascadeLinkId = 0
	}
	return nil
}
