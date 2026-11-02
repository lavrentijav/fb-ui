package groups_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/groups"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

const (
	uidAlice = "11111111-2222-4333-8444-555555555555"
	uidBob   = "99999999-8888-4777-8666-555555555555"
)

func TestSaveGetListDelete(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	g, err := groups.Save(ctx, h, groups.Group{Name: "vip", Remark: "paying", Emails: []string{"alice", " alice ", "", "bob"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Emails) != 2 || g.Emails[0] != "alice" || g.Emails[1] != "bob" {
		t.Fatalf("normalized %v", g.Emails)
	}
	if _, err := groups.Save(ctx, h, groups.Group{Name: "vip"}, false); !errors.Is(err, groups.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := groups.Save(ctx, h, groups.Group{Name: "vip", Emails: []string{"carol"}}, true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, ok := groups.Get(h, "vip"); !ok || len(got.Emails) != 1 || got.Emails[0] != "carol" {
		t.Fatalf("get %+v %v", got, ok)
	}
	if list := groups.List(h); len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	if err := groups.Delete(ctx, h, "vip"); err != nil {
		t.Fatal(err)
	}
	if err := groups.Delete(ctx, h, "vip"); !errors.Is(err, groups.ErrNoGroup) {
		t.Fatalf("delete twice: %v", err)
	}
	for _, bad := range []string{"", " x", "-lead", "a/b"} {
		if err := (groups.Group{Name: bad}).Validate(); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestSetMembers(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	_, _ = groups.Save(ctx, h, groups.Group{Name: "g", Emails: []string{"alice"}}, false)
	if _, err := groups.SetMembers(ctx, h, "g", []string{"bob", "alice"}, true); err != nil {
		t.Fatal(err)
	}
	if g, _ := groups.Get(h, "g"); len(g.Emails) != 2 {
		t.Fatalf("%v", g.Emails)
	}
	if _, err := groups.SetMembers(ctx, h, "g", []string{"alice"}, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := groups.Get(h, "g"); len(g.Emails) != 1 || g.Emails[0] != "bob" {
		t.Fatalf("%v", g.Emails)
	}
	if _, err := groups.SetMembers(ctx, h, "g", []string{" "}, true); !errors.Is(err, groups.ErrNoEmail) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := groups.SetMembers(ctx, h, "nope", []string{"bob"}, true); !errors.Is(err, groups.ErrNoGroup) {
		t.Fatalf("missing: %v", err)
	}
}

func seedClients(t *testing.T, h *modtest.Host, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []clients.Client{
		{Node: "ru4", Inbound: "vpn", Email: "alice", UUID: uidAlice, Enable: true},
		{Node: "de2", Inbound: "vpn", Email: "alice", UUID: uidAlice, Enable: true},
		{Node: "ru4", Inbound: "vpn", Email: "bob", UUID: uidBob, Enable: false, ExpiresAt: now.UnixMilli() - 1},
	} {
		if _, err := clients.Create(ctx, h, c); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = h.Set(ctx, usage.Table, "alice", map[string]any{"bytes": int64(400)})
	_, _ = h.Set(ctx, usage.Table, "bob", map[string]any{"bytes": int64(100)})
}

func TestSummarize(t *testing.T) {
	h := modtest.NewHost("m1")
	now := time.Unix(1_700_000_000, 0)
	seedClients(t, h, now)
	ctx := context.Background()
	_, _ = groups.Save(ctx, h, groups.Group{Name: "g", Emails: []string{"alice", "bob", "ghost"}}, false)

	sum := groups.Summarize(h, groups.Group{Name: "g", Emails: []string{"alice", "bob", "ghost"}}, now)
	if sum.Records != 3 || sum.Enabled != 2 || sum.Disabled != 1 || sum.Expired != 1 {
		t.Fatalf("%+v", sum)
	}
	// usage is per email, so alice's 400 counts once, not once per record
	if sum.TrafficUsed != 500 {
		t.Fatalf("traffic %d", sum.TrafficUsed)
	}
}

func TestSetEnabledAndAttachDetach(t *testing.T) {
	h := modtest.NewHost("m1")
	now := time.Unix(1_700_000_000, 0)
	seedClients(t, h, now)
	ctx := context.Background()
	_, _ = groups.Save(ctx, h, groups.Group{Name: "g", Emails: []string{"alice", "bob"}}, false)

	// disable: alice's two records change, bob's already disabled
	n, err := groups.SetEnabled(ctx, h, "g", false)
	if err != nil || n != 2 {
		t.Fatalf("disable %d %v", n, err)
	}
	if c, _ := clients.Get(h, "de2", "vpn", "alice"); c.Enable {
		t.Fatal("a record stayed enabled")
	}
	if n, err := groups.SetEnabled(ctx, h, "g", false); err != nil || n != 0 {
		t.Fatalf("idempotent disable %d %v", n, err)
	}
	if n, err := groups.SetEnabled(ctx, h, "g", true); err != nil || n != 3 {
		t.Fatalf("enable %d %v", n, err)
	}

	// attach to a new inbound: alice keeps her uuid, bob his
	created, err := groups.Attach(ctx, h, "g", "ru7", "vpn", uidBob)
	if err != nil || created != 2 {
		t.Fatalf("attach %d %v", created, err)
	}
	if again, err := groups.Attach(ctx, h, "g", "ru7", "vpn", uidBob); err != nil || again != 0 {
		t.Fatalf("second attach %d %v", again, err)
	}
	if c, ok := clients.Get(h, "ru7", "vpn", "alice"); !ok || c.UUID != uidAlice || !c.Enable {
		t.Fatalf("alice on ru7 %+v %v", c, ok)
	}

	removed, err := groups.Detach(ctx, h, "g", "ru7", "vpn")
	if err != nil || removed != 2 {
		t.Fatalf("detach %d %v", removed, err)
	}
	if _, ok := clients.Get(h, "ru7", "vpn", "alice"); ok {
		t.Fatal("not detached")
	}
}

func TestAttachNeedsAnIdentity(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	_, _ = groups.Save(ctx, h, groups.Group{Name: "g", Emails: []string{"newbie"}}, false)
	if _, err := groups.Attach(ctx, h, "g", "ru4", "vpn", ""); !errors.Is(err, clients.ErrUUID) {
		t.Fatalf("err %v", err)
	}
}
