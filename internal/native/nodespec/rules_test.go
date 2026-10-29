package nodespec

import (
	"encoding/json"
	"testing"
)

func routing(t *testing.T, s Spec) []map[string]any {
	t.Helper()
	b, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(b.Xray, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Routing.Rules
}

// A rule matches before the inbound's own route, so it overrides the default.
func TestRulesComeBeforeTheInboundRoutes(t *testing.T) {
	s := entrySpec()
	s.Rules = []Rule{
		{Name: "block ads", Inbounds: []string{"de"}, Domains: []string{"geosite:category-ads-all"}, Route: Block},
		{Name: "ru direct", Domains: []string{"regexp:\\.ru$"}, IPs: []string{"geoip:ru", "10.1.0.0/16"}, Route: Direct},
	}
	rules := routing(t, s)
	if len(rules) != 5 { // api, 2 rules, 2 inbound routes
		t.Fatalf("rules = %v", rules)
	}
	if rules[1]["outboundTag"] != Block || rules[1]["domain"].([]any)[0] != "geosite:category-ads-all" || rules[1]["inboundTag"].([]any)[0] != "de" {
		t.Fatalf("first rule = %v", rules[1])
	}
	if rules[2]["outboundTag"] != Direct || len(rules[2]["ip"].([]any)) != 2 {
		t.Fatalf("second rule = %v", rules[2])
	}
	if _, has := rules[2]["inboundTag"]; has {
		t.Fatalf("a rule with no inbounds applies to all: %v", rules[2])
	}
	if rules[3]["inboundTag"].([]any)[0] != "de" || rules[3]["outboundTag"] != ProxyTag("de") {
		t.Fatalf("the default route moved: %v", rules[3])
	}
}

func TestRuleToABalancedExitUsesTheBalancer(t *testing.T) {
	s := entrySpec()
	s.Exits[0].Hops = append(s.Exits[0].Hops, s.Exits[0].Hops[0])
	s.Rules = []Rule{{Domains: []string{"example.com"}, Route: ProxyTag("de")}}
	rules := routing(t, s)
	if rules[1]["balancerTag"] != ProxyTag("de") || rules[1]["outboundTag"] != nil {
		t.Fatalf("rule = %v", rules[1])
	}
}

func TestRuleValidation(t *testing.T) {
	cases := map[string]Rule{
		"unknown route":   {Domains: []string{"a.example"}, Route: "proxy_fr"},
		"matches nothing": {Route: Block},
		"unknown inbound": {Inbounds: []string{"nope"}, Domains: []string{"a.example"}, Route: Block},
		"empty matcher":   {Domains: []string{""}, Route: Block},
		"api as a target": {Inbounds: []string{"api"}, Domains: []string{"a.example"}, Route: Block},
	}
	for name, r := range cases {
		s := entrySpec()
		s.Rules = []Rule{r}
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
