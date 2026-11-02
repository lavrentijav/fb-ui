package settings_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
)

func TestGetFallsBackToDefaults(t *testing.T) {
	h := modtest.NewHost("m1")
	if got := settings.Get(h, settings.Fleet, "notify.telegram.enable"); got != "false" {
		t.Fatalf("default %q", got)
	}
	if n := settings.Int(h, settings.Fleet, "notify.email.port"); n != 587 {
		t.Fatalf("port %d", n)
	}
	if _, unknown := settings.Fleet["nope"]; unknown {
		t.Fatal("schema has an unexpected key")
	}
}

func TestValidateAndMaskSecrets(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")

	if err := settings.Set(ctx, h, settings.Fleet, "notify.telegram.token", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h, settings.Fleet, "notify.telegram.token", "not-a-token"); err == nil {
		t.Fatal("a bad token was accepted")
	}
	// an empty secret keeps the stored one
	if err := settings.Set(ctx, h, settings.Fleet, "notify.telegram.token", ""); err != nil {
		t.Fatal(err)
	}
	if got := settings.Get(h, settings.Fleet, "notify.telegram.token"); got == "" {
		t.Fatal("an empty save erased the secret")
	}

	views := settings.All(h, settings.Fleet)
	var tok settings.View
	for _, v := range views {
		if v.Name == "notify.telegram.token" {
			tok = v
		}
		if v.Kind == settings.KindSecret && v.Value != "" {
			t.Fatalf("a secret leaked: %+v", v)
		}
	}
	if !tok.Secret || !tok.HasSecret || tok.Value != "" {
		t.Fatalf("token view %+v", tok)
	}

	for name, bad := range map[string]string{
		"notify.email.port":     "0",
		"notify.email.tls":      "yes",
		"notify.discord.url":    "ftp://x",
		"notify.telegram.chats": "1,2, 3",
	} {
		if err := settings.Set(ctx, h, settings.Fleet, name, bad); err != nil && name == "notify.telegram.chats" {
			t.Fatalf("%s: %v", name, err) // lists accept anything, blanks dropped
		} else if name != "notify.telegram.chats" && err == nil {
			t.Fatalf("%s accepted %q", name, bad)
		}
	}
	if got := settings.List(h, settings.Fleet, "notify.telegram.chats"); strings.Join(got, "|") != "1|2|3" {
		t.Fatalf("chats %v", got)
	}
	if err := settings.Set(ctx, h, settings.Fleet, "nope", "x"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

func TestSetManyIsOrdered(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	if err := settings.SetMany(ctx, h, settings.Fleet, map[string]string{
		"notify.email.host": "smtp.example", "notify.email.port": "2525", "notify.email.enable": "true",
	}); err != nil {
		t.Fatal(err)
	}
	if settings.Int(h, settings.Fleet, "notify.email.port") != 2525 || !settings.Bool(h, settings.Fleet, "notify.email.enable") {
		t.Fatal("not stored")
	}
	if err := settings.SetMany(ctx, h, settings.Fleet, map[string]string{"notify.email.port": "0"}); err == nil {
		t.Fatal("a bad value in a batch was accepted")
	}
}
