package inboundparam_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/inboundparam"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func TestSetGetMergeDelete(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	node, tag := "ru", "n13-in-12053-tcp"

	if _, ok := inboundparam.Get(h, node, tag); ok {
		t.Fatal("a missing row was reported")
	}
	if err := inboundparam.Set(ctx, h, node, tag, inboundparam.Params{"reserved": "true", "label": "de-fra"}); err != nil {
		t.Fatal(err)
	}
	got, ok := inboundparam.Get(h, node, tag)
	if !ok || got["reserved"] != "true" || got["label"] != "de-fra" {
		t.Fatalf("params %v %v", got, ok)
	}

	merged, err := inboundparam.Merge(ctx, h, node, tag, inboundparam.Params{"reserved": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if merged["reserved"] != "false" || merged["label"] != "de-fra" {
		t.Fatalf("merged %v", merged)
	}

	if _, err := inboundparam.DeleteKey(ctx, h, node, tag, "label"); err != nil {
		t.Fatal(err)
	}
	if got, _ := inboundparam.Get(h, node, tag); got["label"] != "" || len(got) != 1 {
		t.Fatalf("after key delete %v", got)
	}

	if list := inboundparam.List(h); len(list) != 1 || list[inboundparam.Key(node, tag)]["reserved"] != "false" {
		t.Fatalf("list %v", list)
	}
	if err := inboundparam.Delete(ctx, h, node, tag); err != nil {
		t.Fatal(err)
	}
	if _, ok := inboundparam.Get(h, node, tag); ok {
		t.Fatal("the row survived delete")
	}
}

func TestValidation(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	for _, c := range []struct {
		name string
		node string
		tag  string
		p    inboundparam.Params
		want error
	}{
		{"bad key", "ru", "vpn", inboundparam.Params{"a b": "x"}, inboundparam.ErrKey},
		{"long value", "ru", "vpn", inboundparam.Params{"ok": strings.Repeat("x", 4097)}, inboundparam.ErrValue},
		{"no node", "", "vpn", inboundparam.Params{}, inboundparam.ErrRef},
		{"no tag", "ru", "", inboundparam.Params{}, inboundparam.ErrRef},
	} {
		if err := inboundparam.Set(ctx, h, c.node, c.tag, c.p); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestReserved(t *testing.T) {
	for value, want := range map[string]bool{
		"true": true, "TRUE": true, "1": true, "yes": true,
		"": false, "false": false, "0": false, "no": false, "off": false,
	} {
		if got := inboundparam.Reserved(inboundparam.Params{"reserved": value}); got != want {
			t.Fatalf("reserved %q = %v, want %v", value, got, want)
		}
	}
	if inboundparam.Reserved(inboundparam.Params{}) {
		t.Fatal("no reserved key should be false")
	}
}
