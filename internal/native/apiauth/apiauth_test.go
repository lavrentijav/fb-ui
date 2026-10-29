package apiauth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func TestCreateStoresOnlyTheHash(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	token, err := apiauth.Create(ctx, h, "ci", module.ScopeAdmin, 0)
	if err != nil || len(token) < 32 {
		t.Fatalf("token %q err %v", token, err)
	}
	for _, key := range h.Keys(apiauth.Table) {
		if strings.Contains(key, token) {
			t.Fatal("the token itself must not be a key")
		}
		rec, _ := h.Get(apiauth.Table, key)
		for _, v := range rec {
			if strings.Contains(string(v), token) {
				t.Fatal("the token itself must not be stored")
			}
		}
	}
	scope, name, ok := apiauth.Verify(h, token)
	if !ok || scope != module.ScopeAdmin || name != "ci" {
		t.Fatalf("Verify = %v %v %v", scope, name, ok)
	}
}

func TestVerifyRejectsUnknownAndExpired(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	now := time.UnixMilli(1_000_000)
	h.Clock = func() time.Time { return now }
	token, _ := apiauth.Create(ctx, h, "short", module.ScopeMonitor, time.Hour)
	if _, _, ok := apiauth.Verify(h, "nope"); ok {
		t.Fatal("unknown token accepted")
	}
	if _, _, ok := apiauth.Verify(h, token); !ok {
		t.Fatal("fresh token rejected")
	}
	now = now.Add(2 * time.Hour)
	if _, _, ok := apiauth.Verify(h, token); ok {
		t.Fatal("expired token accepted")
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		have, need module.Scope
		want       bool
	}{
		{module.ScopeAdmin, module.ScopeAdmin, true},
		{module.ScopeAdmin, module.ScopeMonitor, true},
		{module.ScopeMonitor, module.ScopeMonitor, true},
		{module.ScopeMonitor, module.ScopeAdmin, false},
		{"", module.ScopeMonitor, false},
	}
	for _, c := range cases {
		if got := apiauth.Allows(c.have, c.need); got != c.want {
			t.Fatalf("Allows(%q,%q)=%v", c.have, c.need, got)
		}
	}
}
