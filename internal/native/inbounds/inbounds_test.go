package inbounds_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/inbounds"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const uid = "11111111-2222-4333-8444-555555555555"

func seed(t *testing.T) (*modtest.Host, string) {
	t.Helper()
	h := modtest.NewHost("m1")
	s := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Host: "ru.example",
			Route: nodespec.Direct,
		}},
		DirectAllow: []string{"10.0.0.0/16"},
	}
	raw, _ := json.Marshal(s)
	if _, err := h.Set(context.Background(), inbounds.SpecTable, "ru4", map[string]any{"spec": json.RawMessage(raw)}); err != nil {
		t.Fatal(err)
	}
	return h, "ru4"
}

func in(tag string, port int) inbounds.Input {
	return inbounds.Input{Tag: tag, Port: port, Network: "ws", Path: "/" + tag, Route: nodespec.Direct}
}

func TestAddUpdateRemove(t *testing.T) {
	h, node := seed(t)
	ctx := context.Background()
	added, err := inbounds.Add(ctx, h, h, node, in("de", 12060))
	if err != nil {
		t.Fatal(err)
	}
	if added.Tag != "de" || len(added.Clients) != 0 {
		t.Fatalf("%+v", added)
	}
	s, _ := inbounds.Spec(h, node)
	if len(s.Inbounds) != 2 {
		t.Fatalf("inbounds %v", s.Inbounds)
	}
	if _, err := inbounds.Add(ctx, h, h, node, in("de", 12061)); !errors.Is(err, inbounds.ErrExists) {
		t.Fatalf("duplicate tag: %v", err)
	}
	if _, err := inbounds.Add(ctx, h, h, node, in("dup", 12054)); err == nil {
		t.Fatal("a duplicate port was accepted")
	}

	// update keeps clients and allows a tag rename
	s.Inbounds[1].Clients = []nodespec.Client{{Email: "alice", ID: uid}}
	raw, _ := json.Marshal(s)
	_, _ = h.Set(ctx, inbounds.SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	up := in("de-new", 12061)
	updated, err := inbounds.Update(ctx, h, h, node, "de", up)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tag != "de-new" || updated.Port != 12061 || len(updated.Clients) != 1 {
		t.Fatalf("%+v", updated)
	}

	// a rule naming the inbound blocks its removal
	s, _ = inbounds.Spec(h, node)
	s.Rules = []nodespec.Rule{{Name: "r", Inbounds: []string{"de-new"}, Domains: []string{"x.example"}, Route: nodespec.Direct}}
	raw, _ = json.Marshal(s)
	_, _ = h.Set(ctx, inbounds.SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	if err := inbounds.Remove(ctx, h, h, node, "de-new"); err == nil {
		t.Fatal("removed an inbound a rule still names")
	}
	s.Rules = nil
	raw, _ = json.Marshal(s)
	_, _ = h.Set(ctx, inbounds.SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	if err := inbounds.Remove(ctx, h, h, node, "de-new"); err != nil {
		t.Fatal(err)
	}
	if err := inbounds.Remove(ctx, h, h, node, "missing"); !errors.Is(err, inbounds.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestValidationAndMissingNode(t *testing.T) {
	h, node := seed(t)
	ctx := context.Background()
	for name, bad := range map[string]inbounds.Input{
		"bad tag":     {Tag: "Bad Tag", Port: 1, Network: "ws", Path: "/x", Route: nodespec.Direct},
		"port 0":      {Tag: "a", Port: 0, Network: "ws", Path: "/x", Route: nodespec.Direct},
		"bad network": {Tag: "a", Port: 1, Network: "tcp", Path: "/x", Route: nodespec.Direct},
		"bad path":    {Tag: "a", Port: 1, Network: "ws", Path: "x", Route: nodespec.Direct},
		"no route":    {Tag: "a", Port: 1, Network: "ws", Path: "/x"},
	} {
		if _, err := inbounds.Add(ctx, h, h, node, bad); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := inbounds.Add(ctx, h, h, "nl9", in("a", 1)); err == nil {
		t.Fatal("a node without a spec was accepted")
	}
	if _, err := inbounds.Add(ctx, h, h, "", in("a", 1)); !errors.Is(err, inbounds.ErrNoNode) {
		t.Fatalf("no node: %v", err)
	}
}

// A write that would not render is refused and leaves the spec alone.
func TestWriteRefusesAnUnrenderableSpec(t *testing.T) {
	h, node := seed(t)
	ctx := context.Background()
	s, _ := inbounds.Spec(h, node)
	s.Inbounds = append(s.Inbounds, nodespec.Inbound{Tag: "broken", Port: 1, Network: "ws", Path: "/x", Route: "nope"})
	if err := inbounds.Write(ctx, h, node, s); err == nil {
		t.Fatal("an unrenderable spec was written")
	}
	got, _ := inbounds.Spec(h, node)
	if len(got.Inbounds) != 1 {
		t.Fatalf("the refused write changed the spec: %v", got.Inbounds)
	}
}

// The old panel's friendly inbound name must survive the API round-trip.
func TestAddKeepsRemark(t *testing.T) {
	h, node := seed(t)
	ctx := context.Background()
	in := in("de", 12060)
	in.Remark = "🇩🇪 Германия Франкфурт"
	if _, err := inbounds.Add(ctx, h, h, node, in); err != nil {
		t.Fatal(err)
	}
	s, _ := inbounds.Spec(h, node)
	var got string
	for _, ib := range s.Inbounds {
		if ib.Tag == "de" {
			got = ib.Remark
		}
	}
	if got != in.Remark {
		t.Fatalf("remark %q", got)
	}
}
