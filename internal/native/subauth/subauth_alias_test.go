package subauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
)

func TestAliasesResolveToThePrimarySubscription(t *testing.T) {
	h, primary := seed(t, "alice", "s3cretpass")
	ctx := context.Background()

	if err := subauth.EnsureAlias(ctx, h, "old-token", primary, true); err != nil {
		t.Fatal(err)
	}
	alias, ok := subauth.Get(h, "old-token")
	if !ok || alias.Email != "alice@example" || alias.AliasOf != primary || !alias.Enable {
		t.Fatalf("alias %+v %v", alias, ok)
	}
	if got := subauth.Aliases(h, primary); len(got) != 1 || got[0] != "old-token" {
		t.Fatalf("aliases %v", got)
	}

	if _, err := h.Set(ctx, subauth.Table, "tok2", map[string]any{"email": "bob@example", "enable": true}); err != nil {
		t.Fatal(err)
	}
	if err := subauth.EnsureAlias(ctx, h, "old-token", "tok2", true); !errors.Is(err, subauth.ErrAliasTaken) {
		t.Fatalf("alias reuse: %v", err)
	}
	if err := subauth.EnsureAlias(ctx, h, "bad/token", primary, true); !errors.Is(err, subauth.ErrAlias) {
		t.Fatalf("bad alias: %v", err)
	}
	if err := subauth.EnsureAlias(ctx, h, "missing-primary", "nope", true); !errors.Is(err, subauth.ErrNoSubscription) {
		t.Fatalf("missing primary: %v", err)
	}
	if err := subauth.DeleteAlias(ctx, h, "old-token", "tok2"); !errors.Is(err, subauth.ErrNoSubscription) {
		t.Fatalf("wrong owner: %v", err)
	}
	if err := subauth.DeleteAlias(ctx, h, "old-token", primary); err != nil {
		t.Fatal(err)
	}
	if _, ok := subauth.Get(h, "old-token"); ok {
		t.Fatal("the alias survived its delete")
	}
}
