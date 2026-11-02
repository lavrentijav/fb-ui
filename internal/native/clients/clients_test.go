package clients_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const uid = "11111111-2222-4333-8444-555555555555"

func base() clients.Client {
	return clients.Client{Node: "ru4", Inbound: "vpn", Email: "alice", UUID: uid, Enable: true}
}

func TestCreateGetListDelete(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	c := base()
	c.TotalGB = 100 << 30
	c.LimitIP = 2
	got, err := clients.Create(ctx, h, c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "alice" || got.UUID != uid {
		t.Fatalf("%+v", got)
	}
	if _, err := clients.Create(ctx, h, c); !errors.Is(err, clients.ErrExists) {
		t.Fatalf("a duplicate: %v", err)
	}
	// Save is the overwrite path
	c.LimitIP = 5
	if _, created, err := clients.Save(ctx, h, c); err != nil || created {
		t.Fatalf("save %v %v", created, err)
	}
	if got, ok := clients.Get(h, "ru4", "vpn", "alice"); !ok || got.LimitIP != 5 {
		t.Fatalf("get %+v %v", got, ok)
	}
	if got := clients.List(h); len(got) != 1 {
		t.Fatalf("list %+v", got)
	}
	if err := clients.Delete(ctx, h, "ru4", "vpn", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := clients.Get(h, "ru4", "vpn", "alice"); ok {
		t.Fatal("not deleted")
	}
}

func TestValidate(t *testing.T) {
	for name, change := range map[string]func(*clients.Client){
		"no node":     func(c *clients.Client) { c.Node = "" },
		"bad inbound": func(c *clients.Client) { c.Inbound = "Bad Tag" },
		"no email":    func(c *clients.Client) { c.Email = "" },
		"slash":       func(c *clients.Client) { c.Email = "a/b" },
		"bad uuid":    func(c *clients.Client) { c.UUID = "nope" },
		"neg bytes":   func(c *clients.Client) { c.TotalGB = -1 },
		"neg limit":   func(c *clients.Client) { c.LimitIP = -1 },
	} {
		c := base()
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("a valid client: %v", err)
	}
}

func TestAttachSkipsDisabledExpiredAndForeignNodes(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	live := base()
	live.Email = "live"
	live.LimitIP = 3
	off := base()
	off.Email, off.Enable = "off", false
	exp := base()
	exp.Email, exp.ExpiresAt = "exp", now.UnixMilli()-1
	future := base()
	future.Email, future.ExpiresAt = "future", now.UnixMilli()+1<<30
	otherNode := base()
	otherNode.Email, otherNode.Node = "elsewhere", "de2"
	for _, c := range []clients.Client{live, off, exp, future, otherNode} {
		if _, err := clients.Create(ctx, h, c); err != nil {
			t.Fatal(err)
		}
	}
	spec := nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{
		{Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct},
		{Tag: "unused", Port: 2, Network: "ws", Path: "/u", Route: nodespec.Direct},
	}}
	clients.Attach(h, "ru4", &spec, now)
	var emails []string
	for _, c := range spec.Inbounds[0].Clients {
		emails = append(emails, c.Email)
	}
	if len(emails) != 2 || emails[0] != "future" || emails[1] != "live" {
		t.Fatalf("attached %v", emails)
	}
	if spec.Inbounds[0].Clients[1].LimitIP != 3 {
		t.Fatalf("limit lost: %+v", spec.Inbounds[0].Clients[1])
	}
	if _, err := nodespec.Render(spec); err != nil {
		t.Fatalf("spec does not render: %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	if n, err := clients.ParseExpiry(""); err != nil || n != 0 {
		t.Fatalf("empty %d %v", n, err)
	}
	if n, err := clients.ParseExpiry("1700000000000"); err != nil || n != 1_700_000_000_000 {
		t.Fatalf("unix %d %v", n, err)
	}
	if n, err := clients.ParseExpiry("2026-01-02T15:04:05Z"); err != nil || n == 0 {
		t.Fatalf("rfc3339 %d %v", n, err)
	}
	if _, err := clients.ParseExpiry("tomorrow"); err == nil {
		t.Fatal("junk accepted")
	}
}

// A client that a virtual network already put on the inbound is not added
// twice, which Render would refuse.
func TestAttachDoesNotDuplicateANetworkMember(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	c := base()
	if _, err := clients.Create(ctx, h, c); err != nil {
		t.Fatal(err)
	}
	spec := nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{{
		Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct,
		Clients: []nodespec.Client{{Email: "alice", ID: uid}},
	}}}
	clients.Attach(h, "ru4", &spec, time.Unix(1_700_000_000, 0))
	if n := len(spec.Inbounds[0].Clients); n != 1 {
		t.Fatalf("attached twice: %d", n)
	}
	if _, err := nodespec.Render(spec); err != nil {
		t.Fatal(err)
	}
}
