package subauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
)

func seed(t *testing.T, user, pass string) (*modtest.Host, string) {
	t.Helper()
	h := modtest.NewHost("m1")
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	hash, err := subauth.Password(pass)
	if err != nil {
		t.Fatal(err)
	}
	token := "tok1"
	if _, err := h.Set(context.Background(), subauth.Table, token, map[string]any{
		"email": "alice@example", "user": user, "pass": hash, "enable": true,
	}); err != nil {
		t.Fatal(err)
	}
	return h, token
}

func TestLoginAndGet(t *testing.T) {
	h, token := seed(t, "alice", "s3cretpass")
	rec, ok := subauth.Get(h, token)
	if !ok || rec.Email != "alice@example" || rec.User != "alice" || !rec.Enable {
		t.Fatalf("%+v %v", rec, ok)
	}
	if got, ok := subauth.ByUser(h, "ALICE"); !ok || got.Token != token {
		t.Fatalf("by user %+v %v", got, ok)
	}
	got, err := subauth.Login(h, "alice", "s3cretpass")
	if err != nil || got.Token != token {
		t.Fatalf("login %+v %v", got, err)
	}
	if _, err := subauth.Login(h, "alice", "wrong"); err != subauth.ErrBadPassword {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := subauth.Login(h, "nobody", "x"); err != subauth.ErrNoUser {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestDisabledSubscriptionCannotLogIn(t *testing.T) {
	h, token := seed(t, "bob", "s3cretpass")
	if _, err := h.Set(context.Background(), subauth.Table, token, map[string]any{
		"email": "alice@example", "user": "bob", "enable": false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := subauth.Login(h, "bob", "s3cretpass"); err != subauth.ErrDisabled {
		t.Fatalf("err %v", err)
	}
	if rec, ok := subauth.Get(h, token); !ok || rec.Enable {
		t.Fatalf("a disabled record: %+v %v", rec, ok)
	}
}

func TestSessions(t *testing.T) {
	h, token := seed(t, "alice", "s3cretpass")
	ctx := context.Background()
	cookie, _, err := subauth.NewSession(ctx, h, token)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_001, 0)
	if rec, ok := subauth.FromCookie(h, cookie, now); !ok || rec.Token != token {
		t.Fatalf("session %+v %v", rec, ok)
	}
	if _, ok := subauth.FromCookie(h, "not-a-session", now); ok {
		t.Fatal("an unknown cookie resolved")
	}
	if err := subauth.EndSession(ctx, h, cookie); err != nil {
		t.Fatal(err)
	}
	if _, ok := subauth.FromCookie(h, cookie, now); ok {
		t.Fatal("a logged-out session resolved")
	}
}

func TestPasswordRules(t *testing.T) {
	gen, err := subauth.NewPassword()
	if err != nil || len(gen) != 16 {
		t.Fatalf("generated %q %v", gen, err)
	}
	if _, err := subauth.Password("short"); err == nil {
		t.Fatal("a short password was accepted")
	}
	hash, err := subauth.Password("longenough")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "longenough" || !subauth.Check(hash, "longenough") || subauth.Check(hash, "other") {
		t.Fatal("hash/check mismatch")
	}
	if subauth.ValidUser("ab") || subauth.ValidUser("a/b") || !subauth.ValidUser("alice.01") {
		t.Fatal("user validation")
	}
}
