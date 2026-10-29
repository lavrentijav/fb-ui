package masterd

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routing"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routingmod"
)

// A rule stored in the op log reaches the node's spec, after the rules the
// spec already carried, and only for the nodes it names.
func TestRoutingRulesReachTheNodeSpec(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	_, _ = h.Set(ctx, routingmod.ListTable, "l1", map[string]any{"name": "ads", "kind": routing.KindDomain, "entries": []string{"ads.example"}, "enable": true})
	_, _ = h.Set(ctx, routingmod.RuleTable, "r1", map[string]any{"name": "block ads", "nodes": []string{"ru4"}, "lists": []string{"l1"}, "action": "block", "order": 1, "enable": true})
	mods := []module.Module{routingmod.New()}

	own := nodespec.Rule{Name: "own", Domains: []string{"own.example"}, Route: nodespec.Direct}
	spec := nodespec.Spec{Node: "ru4", Rules: []nodespec.Rule{own}}
	contribute(mods, h, "ru4", &spec)
	if len(spec.Rules) != 2 || spec.Rules[0].Name != "own" || spec.Rules[1].Name != "block ads" || spec.Rules[1].Domains[0] != "ads.example" {
		t.Fatalf("ru4 rules = %+v", spec.Rules)
	}
	other := nodespec.Spec{Node: "de2"}
	contribute(mods, h, "de2", &other)
	if len(other.Rules) != 0 {
		t.Fatalf("de2 got rules meant for ru4: %+v", other.Rules)
	}
}
