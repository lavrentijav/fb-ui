package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func initFilterTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	db := database.GetDB()
	for _, m := range []any{&model.FilterRule{}, &model.FilterList{}, &model.CascadeLink{}, &model.Node{}} {
		if err := db.Where("1 = 1").Delete(m).Error; err != nil {
			t.Fatalf("clear table: %v", err)
		}
	}
}

// Entries are stored verbatim for Xray, so anything it would reject at config
// load has to be caught here — the alternative is a proxy that will not start.
func TestFilterListEntryValidation(t *testing.T) {
	initFilterTestDB(t)
	s := FilterService{}

	tests := []struct {
		name    string
		kind    string
		entries []string
		wantErr string
	}{
		{name: "plain domains", kind: model.FilterKindDomain, entries: []string{"doubleclick.net"}},
		{name: "domain prefixes", kind: model.FilterKindDomain, entries: []string{"geosite:category-ads-all", "regexp:.*\\.ads\\.com", "full:exact.example.com"}},
		{name: "ip forms", kind: model.FilterKindIP, entries: []string{"10.0.0.1", "185.51.100.0/24", "geoip:ru"}},
		{name: "unknown domain prefix", kind: model.FilterKindDomain, entries: []string{"geosote:typo"}, wantErr: "unknown prefix"},
		{name: "empty prefix value", kind: model.FilterKindDomain, entries: []string{"geosite:"}, wantErr: "needs a value"},
		{name: "url in a domain list", kind: model.FilterKindDomain, entries: []string{"example.com/path"}, wantErr: "not a domain"},
		{name: "domain in an ip list", kind: model.FilterKindIP, entries: []string{"example.com"}, wantErr: "not an IP"},
		{name: "geoip without a category", kind: model.FilterKindIP, entries: []string{"geoip:"}, wantErr: "needs a category"},
		{name: "entry with a comma", kind: model.FilterKindDomain, entries: []string{"a.com,b.com"}, wantErr: "spaces or commas"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := &model.FilterList{
				Name: tt.name, Kind: tt.kind, Entries: tt.entries, Enable: true,
			}
			err := s.CreateList(list)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CreateList: %v", err)
				}
				if len(list.Entries) != len(tt.entries) {
					t.Fatalf("stored %d entries, want %d", len(list.Entries), len(tt.entries))
				}
				return
			}
			if err == nil {
				t.Fatalf("CreateList accepted %v", tt.entries)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tt.wantErr)
			}
			_ = i
		})
	}
}

func TestFilterListNormalizesEntries(t *testing.T) {
	initFilterTestDB(t)
	list := &model.FilterList{
		Name: "ads", Kind: model.FilterKindDomain, Enable: true,
		Entries: []string{" doubleclick.net ", "doubleclick.net", "", "# a comment", "ads.example.com"},
	}
	if err := (&FilterService{}).CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	want := []string{"doubleclick.net", "ads.example.com"}
	if len(list.Entries) != len(want) {
		t.Fatalf("entries = %v, want %v", list.Entries, want)
	}
	for i := range want {
		if list.Entries[i] != want[i] {
			t.Fatalf("entries = %v, want %v", list.Entries, want)
		}
	}
}

// Deleting a list a rule still points at would leave the rule matching nothing,
// which reads as the filter silently breaking.
func TestFilterListDeleteRefusedWhileReferenced(t *testing.T) {
	initFilterTestDB(t)
	s := FilterService{}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"ads.example.com"}, Enable: true}
	if err := s.CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	rule := &model.FilterRule{Name: "block ads", PanelId: SelfPanelId, ListIds: []int{list.Id}, Action: model.FilterActionBlock, Enable: true}
	if err := s.CreateRule(rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	err := s.DeleteList(list.Id)
	if err == nil {
		t.Fatal("DeleteList removed a list a rule still references")
	}
	if !strings.Contains(err.Error(), "block ads") {
		t.Fatalf("error = %q, want it to name the rule", err.Error())
	}

	if err := s.DeleteRule(rule.Id); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if err := s.DeleteList(list.Id); err != nil {
		t.Fatalf("DeleteList after the rule is gone: %v", err)
	}
}

func TestFilterRuleValidation(t *testing.T) {
	initFilterTestDB(t)
	s := FilterService{}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"ads.example.com"}, Enable: true}
	if err := s.CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	tests := []struct {
		name    string
		rule    model.FilterRule
		wantErr string
	}{
		{
			name:    "no lists",
			rule:    model.FilterRule{Name: "empty", PanelId: SelfPanelId, Action: model.FilterActionBlock},
			wantErr: "at least one list",
		},
		{
			name:    "unknown list",
			rule:    model.FilterRule{Name: "ghost", PanelId: SelfPanelId, ListIds: []int{9999}, Action: model.FilterActionBlock},
			wantErr: "not found",
		},
		{
			name:    "unknown panel",
			rule:    model.FilterRule{Name: "nowhere", PanelId: 4242, ListIds: []int{list.Id}, Action: model.FilterActionBlock},
			wantErr: "panel not found",
		},
		{
			name:    "cascade without a link",
			rule:    model.FilterRule{Name: "divert", PanelId: SelfPanelId, ListIds: []int{list.Id}, Action: model.FilterActionCascade},
			wantErr: "needs the link",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := tt.rule
			err := s.CreateRule(&rule)
			if err == nil {
				t.Fatal("CreateRule accepted an invalid rule")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tt.wantErr)
			}
		})
	}

	// An unknown action falls back to the safe one rather than reaching Xray.
	rule := &model.FilterRule{Name: "typo action", PanelId: SelfPanelId, ListIds: []int{list.Id}, Action: "drop", Enable: true}
	if err := s.CreateRule(rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if rule.Action != model.FilterActionBlock {
		t.Fatalf("action = %q, want it defaulted to block", rule.Action)
	}
}

// The graph is what the editor draws, so a rule has to reach it as a vertex.
func TestNetworkGraphCarriesFilters(t *testing.T) {
	initFilterTestDB(t)
	s := FilterService{}
	list := &model.FilterList{Name: "ads", Kind: model.FilterKindDomain, Entries: []string{"ads.example.com"}, Enable: true}
	if err := s.CreateList(list); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if err := s.CreateRule(&model.FilterRule{
		Name: "block ads", PanelId: SelfPanelId, ListIds: []int{list.Id},
		Action: model.FilterActionBlock, Enable: true,
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	graph, err := (&NetworkService{}).Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(graph.Filters) != 1 || graph.Filters[0].Name != "block ads" {
		t.Fatalf("graph filters = %+v, want the stored rule", graph.Filters)
	}
	if len(graph.Lists) != 1 || graph.Lists[0].Name != "ads" {
		t.Fatalf("graph lists = %+v, want the referenced list", graph.Lists)
	}
}
