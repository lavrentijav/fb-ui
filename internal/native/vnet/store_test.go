package vnet_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func fleet(t *testing.T) map[string]*vnet.Store {
	t.Helper()
	buses, _ := bustest.Fleet(t)
	out := map[string]*vnet.Store{}
	for _, name := range bustest.Masters {
		mem := master.NewMem()
		m, err := master.New(context.Background(), name, bustest.Masters, buses[name], mem)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &vnet.Store{Master: m, State: mem.State, Settle: 200 * time.Millisecond}
	}
	return out
}

// until waits for every master to see ok, catching up as it goes.
func until(t *testing.T, stores map[string]*vnet.Store, what string, ok func(s *vnet.Store) bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		all := true
		for _, s := range stores {
			_, _ = s.Master.CatchUp(context.Background())
			all = all && ok(s)
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("masters never agreed: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestNetworkLifecycle(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	fi1, ru2, de2 := st["fi1"], st["ru2"], st["de2"]
	cgnat := netip.MustParsePrefix("100.64.0.0/10")

	alice, err := fi1.CreateUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := ru2.CreateUser(ctx, "bob")
	carol, _ := de2.CreateUser(ctx, "carol")
	for _, u := range []vnet.User{alice, bob, carol} {
		ip, err := netip.ParseAddr(u.IP)
		if err != nil || !cgnat.Contains(ip) || u.UUID == "" || len(u.Token) < 32 {
			t.Fatalf("user %+v", u)
		}
	}
	if alice.IP == bob.IP || bob.IP == carol.IP || alice.IP == carol.IP {
		t.Fatal("two users share an address")
	}
	until(t, st, "users replicated", func(s *vnet.Store) bool { return len(s.Users()) == 3 })
	if u, ok := de2.UserByToken(alice.Token); !ok || u.ID != "alice" {
		t.Fatalf("token lookup on another master: %+v %v", u, ok)
	}

	netID, err := fi1.CreateNetwork(ctx, "alice", "друзья")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fi1.CreateNetwork(ctx, "alice", "вторая"); !errors.Is(err, vnet.ErrInNetwork) {
		t.Fatalf("second network: %v", err)
	}
	code, err := fi1.Invite(ctx, "alice", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	until(t, st, "invite replicated", func(s *vnet.Store) bool { _, ok := s.State.Get(vnet.InviteTable, code); return ok })
	if err := ru2.Join(ctx, "bob", code); err != nil {
		t.Fatalf("bob joins through ru2: %v", err)
	}
	until(t, st, "bob's membership", func(s *vnet.Store) bool { return len(s.Members(netID)) == 2 })
	if err := de2.Join(ctx, "carol", code); !errors.Is(err, vnet.ErrBadInvite) {
		t.Fatalf("a used-up invite let carol in: %v", err)
	}
	if _, err := ru2.Invite(ctx, "bob", time.Hour, 1); !errors.Is(err, vnet.ErrForbidden) {
		t.Fatalf("a member invited: %v", err)
	}
	if err := fi1.Leave(ctx, "alice"); !errors.Is(err, vnet.ErrOwnerLeaves) {
		t.Fatalf("owner left: %v", err)
	}
	if err := fi1.SetRole(ctx, "alice", "bob", "admin"); err != nil {
		t.Fatal(err)
	}
	until(t, st, "bob is admin", func(s *vnet.Store) bool { m, _ := s.NetworkOf("bob"); return m.Role == "admin" })
	code2, err := ru2.Invite(ctx, "bob", time.Hour, 5)
	if err != nil {
		t.Fatalf("admin invites: %v", err)
	}
	until(t, st, "second invite", func(s *vnet.Store) bool { _, ok := s.State.Get(vnet.InviteTable, code2); return ok })
	if err := de2.Join(ctx, "carol", code2); err != nil {
		t.Fatal(err)
	}
	until(t, st, "carol in", func(s *vnet.Store) bool { return len(s.Members(netID)) == 3 })
	if err := ru2.Kick(ctx, "bob", "alice"); !errors.Is(err, vnet.ErrForbidden) {
		t.Fatalf("admin kicked the owner: %v", err)
	}
	if err := fi1.Transfer(ctx, "alice", "carol"); err != nil {
		t.Fatal(err)
	}
	until(t, st, "carol owns", func(s *vnet.Store) bool { m, _ := s.NetworkOf("carol"); return m.Role == "owner" })
	if err := de2.DeleteNetwork(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	until(t, st, "network gone", func(s *vnet.Store) bool {
		_, a := s.NetworkOf("alice")
		_, b := s.NetworkOf("bob")
		return !a && !b && len(s.Networks()) == 0
	})
}

// Review focus 4: one user joining two networks through two masters at
// once ends in one membership, the same on every master.
func TestOneNetworkPerUserUnderRace(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := st["fi1"].CreateUser(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	na, _ := st["fi1"].CreateNetwork(ctx, "a", "A")
	nb, _ := st["fi1"].CreateNetwork(ctx, "b", "B")
	ca, _ := st["fi1"].Invite(ctx, "a", time.Hour, 1)
	cb, _ := st["fi1"].Invite(ctx, "b", time.Hour, 1)
	until(t, st, "invites everywhere", func(s *vnet.Store) bool {
		_, a := s.State.Get(vnet.InviteTable, ca)
		_, b := s.State.Get(vnet.InviteTable, cb)
		return a && b
	})
	errs := make(chan error, 2)
	go func() { errs <- st["ru2"].Join(ctx, "c", ca) }()
	go func() { errs <- st["de2"].Join(ctx, "c", cb) }()
	<-errs
	<-errs
	var seen string
	until(t, st, "one membership", func(s *vnet.Store) bool {
		m, ok := s.NetworkOf("c")
		if !ok || (m.Network != na && m.Network != nb) {
			return false
		}
		if seen == "" {
			seen = m.Network
		}
		return m.Network == seen
	})
	in := 0
	for _, n := range []string{na, nb} {
		for _, m := range st["fi1"].Members(n) {
			if m.User == "c" {
				in++
			}
		}
	}
	if in != 1 {
		t.Fatalf("c is in %d networks", in)
	}
}
