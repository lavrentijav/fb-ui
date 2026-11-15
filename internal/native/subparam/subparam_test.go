package subparam_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subparam"
)

func TestSetGetMergeDelete(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	token := "tok-1"

	if _, ok := subparam.Get(h, token); ok {
		t.Fatal("a missing row was reported")
	}
	if err := subparam.Set(ctx, h, token, subparam.Params{"flag": "on", "plan": "pro"}); err != nil {
		t.Fatal(err)
	}
	got, ok := subparam.Get(h, token)
	if !ok || got["flag"] != "on" || got["plan"] != "pro" {
		t.Fatalf("params %v %v", got, ok)
	}

	merged, err := subparam.Merge(ctx, h, token, subparam.Params{"flag": "off"})
	if err != nil {
		t.Fatal(err)
	}
	if merged["flag"] != "off" || merged["plan"] != "pro" {
		t.Fatalf("merged %v", merged)
	}
	if got, _ := subparam.Get(h, token); got["flag"] != "off" || got["plan"] != "pro" {
		t.Fatalf("stored after merge %v", got)
	}

	if _, err := subparam.DeleteKey(ctx, h, token, "plan"); err != nil {
		t.Fatal(err)
	}
	if got, _ := subparam.Get(h, token); got["plan"] != "" || len(got) != 1 {
		t.Fatalf("after key delete %v", got)
	}

	if list := subparam.List(h); len(list) != 1 || list[token]["flag"] != "off" {
		t.Fatalf("list %v", list)
	}
	if err := subparam.Delete(ctx, h, token); err != nil {
		t.Fatal(err)
	}
	if _, ok := subparam.Get(h, token); ok {
		t.Fatal("the row survived delete")
	}
	if list := subparam.List(h); len(list) != 0 {
		t.Fatalf("list after delete %v", list)
	}
}

func TestValidation(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	for _, c := range []struct {
		name string
		p    subparam.Params
		want error
	}{
		{"bad key", subparam.Params{"a b": "x"}, subparam.ErrKey},
		{"long value", subparam.Params{"ok": strings.Repeat("x", 4097)}, subparam.ErrValue},
	} {
		if err := subparam.Set(ctx, h, "tok", c.p); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	tooMany := subparam.Params{}
	for i := 0; i <= subparam.MaxParams; i++ {
		tooMany[string(rune('a'+i%26))+strings.Repeat("x", i/26)] = "1"
	}
	if err := subparam.Set(ctx, h, "tok", tooMany); !errors.Is(err, subparam.ErrTooMany) {
		t.Fatalf("too many: %v", err)
	}
	if err := subparam.Set(ctx, h, "", subparam.Params{}); !errors.Is(err, subparam.ErrToken) {
		t.Fatalf("empty token: %v", err)
	}
}
