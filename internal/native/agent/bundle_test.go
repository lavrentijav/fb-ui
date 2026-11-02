package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// A relay runs no core: applying a bundle with only edge routes and a
// certificate must not touch Xray.
func TestRelayApplierIgnoresTheCore(t *testing.T) {
	edge := &edgerun.Runner{Listen: "127.0.0.1:0"}
	defer edge.Close()
	b := &BundleApplier{Edge: edge, Node: "relay1"}
	bundle := nodespec.Bundle{
		Xray:  []byte(`{}`),
		Edge:  nodespec.Edge{SNI: []nodespec.SNIRoute{{Match: "a.example", Action: "tcp", Target: "10.0.0.9:443"}}},
		Drain: nodespec.Drain{On: false},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(context.Background(), desired.State{Config: raw}); err != nil {
		t.Fatalf("relay apply: %v", err)
	}
	// a later ban change also must not need a core
	if err := b.SetBans(context.Background(), []string{"10.1.2.3"}); err != nil {
		t.Fatalf("relay bans: %v", err)
	}
}

// A node stops a spent client in its running config without touching the
// other users, and restores it when the decision is withdrawn.
func TestSetDisabledRewritesTheRunningConfig(t *testing.T) {
	cfg := `{"inbounds":[{"tag":"vpn","settings":{"clients":[
	  {"id":"a","email":"alice"},{"id":"b","email":"bob"}]}}],
	 "outbounds":[{"tag":"direct"}]}`
	b := &BundleApplier{Node: "ru4"}
	b.last = &nodespec.Bundle{Xray: []byte(cfg), Edge: nodespec.Edge{}}
	// with no core, SetDisabled only records the set (relay case)
	if err := b.SetDisabled(context.Background(), []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if got := b.Disabled(); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("disabled %v", got)
	}
	out, err := withoutClients([]byte(cfg), map[string]bool{"bob": true})
	if err != nil || strings.Contains(string(out), `"bob"`) || !strings.Contains(string(out), `"alice"`) {
		t.Fatalf("filtered config %s", out)
	}
}
