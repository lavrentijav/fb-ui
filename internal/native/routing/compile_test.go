package routing_test

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routing"
)

var lists = []routing.List{
	{ID: "ads", Kind: routing.KindDomain, Entries: []string{"geosite:category-ads-all"}, Enable: true},
	{ID: "ru", Kind: routing.KindDomain, Entries: []string{`regexp:\.ru$`}, Enable: true},
	{ID: "ruip", Kind: routing.KindIP, Entries: []string{"geoip:ru"}, Remote: []string{"5.0.0.0/8"}, Enable: true},
	{ID: "asn", Kind: routing.KindASN, Entries: []string{"AS13238"}, Remote: []string{"213.180.192.0/19"}, Enable: true},
	{ID: "pending", Kind: routing.KindASN, Entries: []string{"AS1"}, Enable: true}, // not resolved yet
	{ID: "off", Kind: routing.KindDomain, Entries: []string{"off.example"}, Enable: false},
}

func TestCompileOrdersFiltersAndSplitsMatchers(t *testing.T) {
	rules := []routing.Rule{
		{ID: "r2", Name: "RU direct", Lists: []string{"ru", "ruip", "asn"}, Action: "direct", Order: 2, Enable: true},
		{ID: "r1", Name: "block ads", Inbounds: []string{"de"}, Lists: []string{"ads"}, Action: "block", Order: 1, Enable: true},
		{ID: "r3", Name: "only ru4", Nodes: []string{"ru4"}, Lists: []string{"ads"}, Action: "block", Order: 3, Enable: true},
		{ID: "r4", Name: "disabled", Lists: []string{"ads"}, Action: "block", Order: 4, Enable: false},
		{ID: "r5", Name: "nothing resolved", Lists: []string{"pending", "off"}, Action: "block", Order: 5, Enable: true},
	}
	got := routing.Compile(lists, rules, "ru2")
	want := []nodespec.Rule{
		{Name: "block ads", Inbounds: []string{"de"}, Domains: []string{"geosite:category-ads-all"}, Route: "block"},
		{Name: "RU direct", Domains: []string{`regexp:\.ru$`}, IPs: []string{"geoip:ru", "5.0.0.0/8", "213.180.192.0/19"}, Route: "direct"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compile(ru2) = %+v\nwant %+v", got, want)
	}
	if n := len(routing.Compile(lists, rules, "ru4")); n != 3 {
		t.Fatalf("ru4 should also get its own rule, got %d rules", n)
	}
}

func TestTestFollowsTheSameOrderAndFallsBackToTheDefault(t *testing.T) {
	rules := []routing.Rule{
		{ID: "r1", Name: "block ads", Lists: []string{"ads"}, Action: "block", Order: 1, Enable: true},
		{ID: "r2", Name: "RU direct", Lists: []string{"ru", "asn"}, Action: "direct", Order: 2, Enable: true},
	}
	v := routing.Test(lists, rules, "ru2", "de", "proxy_de", "mail.yandex.ru")
	if v.Default || v.Route != "direct" || v.Rule != "RU direct" || len(v.Steps) != 2 || v.Steps[0].Matched || !v.Steps[0].Maybe {
		t.Fatalf("verdict = %+v", v)
	}
	if v := routing.Test(lists, rules, "ru2", "de", "proxy_de", "213.180.200.1"); v.Route != "direct" || v.Default {
		t.Fatalf("an address inside the ASN's prefix must go direct: %+v", v)
	}
	v = routing.Test(lists, rules, "ru2", "de", "proxy_de", "example.com")
	if !v.Default || v.Route != "proxy_de" || v.Rule != "" {
		t.Fatalf("no rule matches: %+v", v)
	}
}
