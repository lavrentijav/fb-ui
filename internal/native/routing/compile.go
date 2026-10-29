package routing

import (
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// List is a named set of matchers. Remote is what a source URL or an ASN
// resolved to, kept apart from the operator's own Entries.
type List struct {
	ID      string
	Name    string
	Kind    string
	Entries []string
	Remote  []string
	Enable  bool
}

// matchers is everything the list contributes to a rule. An ASN list matches
// only through its resolved prefixes: its entries are AS numbers, which Xray
// cannot match.
func (l List) matchers() []string {
	if l.Kind == KindASN {
		return l.Remote
	}
	return append(slices.Clone(l.Entries), l.Remote...)
}

// Rule applies lists to traffic. Nodes and Inbounds narrow where; empty means
// everywhere. Action is direct, block or proxy_<country>.
type Rule struct {
	ID       string
	Name     string
	Nodes    []string
	Inbounds []string
	Lists    []string
	Action   string
	Order    int
	Enable   bool
}

func (r Rule) appliesTo(node string) bool {
	return r.Enable && (len(r.Nodes) == 0 || slices.Contains(r.Nodes, node))
}

func ordered(rules []Rule, node string) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.appliesTo(node) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func index(lists []List) map[string]List {
	m := make(map[string]List, len(lists))
	for _, l := range lists {
		m[l.ID] = l
	}
	return m
}

// split sorts a rule's matchers into Xray's two arrays; a disabled or missing
// list contributes nothing.
func split(r Rule, byID map[string]List) (domains, ips []string) {
	for _, id := range r.Lists {
		l, ok := byID[id]
		if !ok || !l.Enable {
			continue
		}
		if l.Kind == KindDomain {
			domains = append(domains, l.matchers()...)
		} else {
			ips = append(ips, l.matchers()...)
		}
	}
	return domains, ips
}

// Compile turns the operator's rules into the node's Spec.Rules. A rule whose
// lists yield no matcher (an ASN not yet resolved) is left out rather than
// sent empty, which would match everything.
func Compile(lists []List, rules []Rule, node string) []nodespec.Rule {
	byID := index(lists)
	var out []nodespec.Rule
	for _, r := range ordered(rules, node) {
		domains, ips := split(r, byID)
		if len(domains)+len(ips) == 0 {
			continue
		}
		out = append(out, nodespec.Rule{Name: r.Name, Inbounds: slices.Clone(r.Inbounds), Domains: domains, IPs: ips, Route: r.Action})
	}
	return out
}

// Step is one rule as a target met it.
type Step struct {
	Rule    string `json:"rule"`
	Route   string `json:"route"`
	Matched bool   `json:"matched"`
	Maybe   bool   `json:"maybe"`
}

// Verdict answers where traffic to a target would go.
type Verdict struct {
	Target  string `json:"target"`
	Route   string `json:"route"`
	Rule    string `json:"rule,omitempty"`
	Default bool   `json:"default"`
	Steps   []Step `json:"steps"`
}

// Test walks the rules the way Compile orders them for traffic entering
// inbound on node and aimed at target. The first certain hit decides; with
// none, the verdict is defaultRoute, the inbound's own route.
func Test(lists []List, rules []Rule, node, inbound, defaultRoute, target string) Verdict {
	target = strings.ToLower(strings.TrimSpace(target))
	byID := index(lists)
	_, parseErr := netip.ParseAddr(target)
	isIP := parseErr == nil
	v := Verdict{Target: target, Steps: []Step{}}
	for _, r := range ordered(rules, node) {
		if inbound != "" && len(r.Inbounds) > 0 && !slices.Contains(r.Inbounds, inbound) {
			continue
		}
		domains, ips := split(r, byID)
		var hit, maybe bool
		if isIP {
			hit, maybe = Match(KindIP, target, ips)
		} else {
			hit, maybe = Match(KindDomain, target, domains)
		}
		v.Steps = append(v.Steps, Step{Rule: r.Name, Route: r.Action, Matched: hit, Maybe: maybe && !hit})
		if hit && v.Rule == "" {
			v.Route, v.Rule = r.Action, r.Name
		}
	}
	if v.Rule == "" {
		v.Default, v.Route = true, defaultRoute
	}
	return v
}
