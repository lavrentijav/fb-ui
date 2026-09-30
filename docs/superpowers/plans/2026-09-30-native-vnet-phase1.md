# Native virtual networks, phase 1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Users on the native fleet share one virtual network each: members reach each other's devices on any port by a permanent `100.64.0.0/10` address, through their own Xray tunnel, wherever each is connected.

**Architecture:** Masters keep users, addresses, networks, members and invites in the op log and serve `/api/v1`. A node bundle carries the network table (members, their addresses, how to reach other nodes); every node publishes who is online on the bus (`presence.<node>`) and merges the fleet's presence with the table into Xray routing, applied live through `xray api` (no restart). Members hold a VLESS reverse tunnel (`u-<email>`) to their node; traffic to a member goes into that tunnel locally or over a cascade to the member's node.

**Tech Stack:** Go 1.27, embedded NATS (`internal/native/bus`), op log + LWW state (`internal/native/master`, `lww`), Xray 26.9.9 VLESS reverse, network lab (`tools/netlab/run.sh`).

**Spec:** `docs/design/native-vnet.md` (phase 1 of its "Order of work": addresses, networks, members, invites, API, presence, in-network routing; tests 1, 2, 4, 5). Exposes by name, allocated ports and limits are phases 2–4, separate plans.

## Global Constraints

- Module `github.com/mhsanaei/3x-ui/v3`; worktree `/home/claud/projects/fb-ui-native`, branch `native`. Never `git stash`.
- Node-side packages (`nodespec`, `vroute`, `presence`, `agent`, `xrayrun`, `cmd/fbnode`) must not import master-side ones (`master`, `vnet`, `masterd`, `pgstate`).
- Internal addresses: `100.64.0.0/10`, one per user, permanent.
- One network per user, own or someone else's (the `vnet_member` table is keyed by user).
- Reverse tag on nodes: `u-<email>`; the client's reverse inbound tag: `from-node`.
- Presence: published every 2 s, an entry expires after 10 s without a refresh; the newest report wins when a user is on two nodes.
- API messages are Russian (the bot shows them as they are).
- Go comments at most 2 lines per block; `golangci-lint run ./internal/native/...` clean; conventional commits ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Every test must fail without its code: after a test passes, break the code, see it fail, restore.

## Review Focus

1. A user connected on two nodes at once (phone and PC): incoming traffic must go to the newest node, and the older node must not keep delivering to its own tunnel — Task 3 pins it.
2. A node dies without saying goodbye: its presence must expire (10 s) so routes stop pointing at it — Task 4 pins it.
3. Xray keeps a reverse outbound (`u-<email>`) after the client disconnects (seen in the lab: `lso` still lists it): routing must never rely on the outbound's existence, only on presence — Task 3 pins it (no rule to `u-x` without presence).
4. The same user joining two networks at once through two masters: the op log must end with one membership, on every master — Task 1 pins it.
5. A member who leaves (or is removed) while connected must lose access at once: the next routing has no rule letting them reach the others — Task 3 and the lab (Task 8) pin it.

---

### Task 1: The masters' records — users, addresses, networks, invites

**Files:**
- Create: `internal/native/vnet/store.go`
- Test: `internal/native/vnet/store_test.go`

**Interfaces:**
- Consumes: `master.Master` (`Name()`, `Set(ctx, table, key, map[string]any) (oplog.Op, error)`, `Delete(ctx, table, key)`, `CatchUp(ctx) (int, error)`); `lww.State` via a `Reader` (`Get(table, key) (map[string]json.RawMessage, bool)`, `Keys(table) []string`).
- Produces (package `vnet`):
  - tables `UserTable = "vuser"`, `TokenTable = "vtoken"`, `AddrTable = "vaddr"`, `NetTable = "vnet"`, `MemberTable = "vnet_member"`, `InviteTable = "vnet_invite"`
  - `type User struct{ ID, Email, UUID, Token, IP string }`
  - `type Member struct{ User, Network, Role string }` (roles `owner`, `admin`, `member`)
  - `type Store struct{ Master *master.Master; State Reader; Now func() time.Time; Settle time.Duration }`
  - `(*Store) CreateUser(ctx, id string) (User, error)`, `User(id string) (User, bool)`, `UserByToken(token string) (User, bool)`, `Users() []User`
  - `(*Store) CreateNetwork(ctx, user, title string) (string, error)`, `NetworkOf(user string) (Member, bool)`, `Members(network string) []Member`, `Networks() []string`
  - `(*Store) Invite(ctx, by string, ttl time.Duration, uses int) (string, error)`, `Revoke(ctx, by, code string) error`, `Join(ctx, user, code string) error`
  - `(*Store) Leave(ctx, user string) error`, `Kick(ctx, by, user string) error`, `SetRole(ctx, by, user, role string) error`, `Transfer(ctx, by, to string) error`, `DeleteNetwork(ctx, by string) error`
  - errors `ErrInNetwork`, `ErrNoNetwork`, `ErrForbidden`, `ErrBadInvite`, `ErrOwnerLeaves`, `ErrNoUser`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/claud/projects/fb-ui-native && go test ./internal/native/vnet/`
Expected: FAIL — `package .../internal/native/vnet is not in std` / undefined `vnet.Store`.

- [ ] **Step 3: Write the implementation**

```go
// Package vnet keeps the users' virtual networks on the masters: users and
// their permanent addresses, networks, members and invites, in the op log.
package vnet

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

const (
	UserTable   = "vuser"
	TokenTable  = "vtoken"
	AddrTable   = "vaddr"
	NetTable    = "vnet"
	MemberTable = "vnet_member" // keyed by user: one network per user
	InviteTable = "vnet_invite"
)

var (
	ErrInNetwork   = errors.New("вы уже состоите в сети: сначала выйдите из неё")
	ErrNoNetwork   = errors.New("вы не состоите в сети")
	ErrForbidden   = errors.New("недостаточно прав")
	ErrBadInvite   = errors.New("приглашение недействительно или закончилось")
	ErrOwnerLeaves = errors.New("владелец не может выйти: удалите сеть или передайте её")
	ErrNoUser      = errors.New("нет такого пользователя")
)

type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

type User struct{ ID, Email, UUID, Token, IP string }

type Member struct{ User, Network, Role string }

type Store struct {
	Master *master.Master
	State  Reader
	Now    func() time.Time
	// Settle is how long a claim replicates before it is checked (default 5 s).
	Settle time.Duration
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func str(rec map[string]json.RawMessage, f string) string {
	var v string
	_ = json.Unmarshal(rec[f], &v)
	return v
}

func num(rec map[string]json.RawMessage, f string) int64 {
	var v int64
	_ = json.Unmarshal(rec[f], &v)
	return v
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// settle lets a write replicate, then reads what won.
func (s *Store) settle(ctx context.Context) error {
	d := s.Settle
	if d <= 0 {
		d = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
	}
	_, err := s.Master.CatchUp(ctx)
	return err
}

// randomAddr is an address in 100.64.0.0/10 that is not a .0 or .255.
func randomAddr() string {
	var b [4]byte
	for {
		_, _ = rand.Read(b[:])
		v := binary.BigEndian.Uint32(b[:])&0x003fffff | 0x64400000
		if last := byte(v); last != 0 && last != 255 {
			var a [4]byte
			binary.BigEndian.PutUint32(a[:], v)
			return netip.AddrFrom4(a).String()
		}
	}
}

// CreateUser gives id a VLESS id, an API token and a permanent address; the
// address is claimed and re-read after it replicates, since two masters can
// hand out the same one at once.
func (s *Store) CreateUser(ctx context.Context, id string) (User, error) {
	if u, ok := s.User(id); ok {
		return u, nil
	}
	u := User{ID: id, Email: id, UUID: uuid4(), Token: random(24)}
	for range 5 {
		ip := randomAddr()
		if _, taken := s.State.Get(AddrTable, ip); taken {
			continue
		}
		if _, err := s.Master.Set(ctx, AddrTable, ip, map[string]any{"user": id}); err != nil {
			return User{}, err
		}
		if err := s.settle(ctx); err != nil {
			return User{}, err
		}
		if rec, _ := s.State.Get(AddrTable, ip); str(rec, "user") == id {
			u.IP = ip
			break
		}
	}
	if u.IP == "" {
		return User{}, errors.New("vnet: no free address")
	}
	if _, err := s.Master.Set(ctx, TokenTable, u.Token, map[string]any{"user": id}); err != nil {
		return User{}, err
	}
	_, err := s.Master.Set(ctx, UserTable, id, map[string]any{
		"email": u.Email, "uuid": u.UUID, "token": u.Token, "ip": u.IP,
	})
	return u, err
}

func (s *Store) User(id string) (User, bool) {
	rec, ok := s.State.Get(UserTable, id)
	if !ok {
		return User{}, false
	}
	return User{ID: id, Email: str(rec, "email"), UUID: str(rec, "uuid"), Token: str(rec, "token"), IP: str(rec, "ip")}, true
}

func (s *Store) UserByToken(token string) (User, bool) {
	rec, ok := s.State.Get(TokenTable, token)
	if !ok || token == "" {
		return User{}, false
	}
	return s.User(str(rec, "user"))
}

func (s *Store) Users() []User {
	var out []User
	for _, id := range s.State.Keys(UserTable) {
		if u, ok := s.User(id); ok {
			out = append(out, u)
		}
	}
	return out
}

func (s *Store) NetworkOf(user string) (Member, bool) {
	rec, ok := s.State.Get(MemberTable, user)
	if !ok {
		return Member{}, false
	}
	m := Member{User: user, Network: str(rec, "network"), Role: str(rec, "role")}
	if _, live := s.State.Get(NetTable, m.Network); !live {
		return Member{}, false // the network was deleted under it
	}
	return m, true
}

func (s *Store) Networks() []string { return s.State.Keys(NetTable) }

func (s *Store) Members(network string) []Member {
	var out []Member
	for _, user := range s.State.Keys(MemberTable) {
		if m, ok := s.NetworkOf(user); ok && m.Network == network {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) join(ctx context.Context, user, network, role string) error {
	_, err := s.Master.Set(ctx, MemberTable, user, map[string]any{"network": network, "role": role})
	return err
}

func (s *Store) CreateNetwork(ctx context.Context, user, title string) (string, error) {
	if _, ok := s.User(user); !ok {
		return "", ErrNoUser
	}
	if _, in := s.NetworkOf(user); in {
		return "", ErrInNetwork
	}
	id := random(8)
	if _, err := s.Master.Set(ctx, NetTable, id, map[string]any{"title": title, "owner": user}); err != nil {
		return "", err
	}
	return id, s.join(ctx, user, id, "owner")
}

func canInvite(m Member) bool { return m.Role == "owner" || m.Role == "admin" }

func (s *Store) Invite(ctx context.Context, by string, ttl time.Duration, uses int) (string, error) {
	m, ok := s.NetworkOf(by)
	if !ok {
		return "", ErrNoNetwork
	}
	if !canInvite(m) {
		return "", ErrForbidden
	}
	code := random(6)
	_, err := s.Master.Set(ctx, InviteTable, code, map[string]any{
		"network": m.Network, "by": by, "expires": s.now().Add(ttl).UnixMilli(), "uses": max(uses, 1),
	})
	return code, err
}

func (s *Store) Revoke(ctx context.Context, by, code string) error {
	m, ok := s.NetworkOf(by)
	rec, found := s.State.Get(InviteTable, code)
	if !ok || !found || str(rec, "network") != m.Network || !canInvite(m) {
		return ErrForbidden
	}
	_, err := s.Master.Delete(ctx, InviteTable, code)
	return err
}

// Join uses up one of the invite's uses; the membership record is keyed by
// user, so two joins at once through two masters leave exactly one.
func (s *Store) Join(ctx context.Context, user, code string) error {
	if _, ok := s.User(user); !ok {
		return ErrNoUser
	}
	if _, in := s.NetworkOf(user); in {
		return ErrInNetwork
	}
	rec, ok := s.State.Get(InviteTable, code)
	if !ok || num(rec, "uses") <= 0 || s.now().UnixMilli() > num(rec, "expires") {
		return ErrBadInvite
	}
	network := str(rec, "network")
	if _, live := s.State.Get(NetTable, network); !live {
		return ErrBadInvite
	}
	if _, err := s.Master.Set(ctx, InviteTable, code, map[string]any{"uses": num(rec, "uses") - 1}); err != nil {
		return err
	}
	return s.join(ctx, user, network, "member")
}

func (s *Store) Leave(ctx context.Context, user string) error {
	m, ok := s.NetworkOf(user)
	if !ok {
		return ErrNoNetwork
	}
	if m.Role == "owner" {
		return ErrOwnerLeaves
	}
	_, err := s.Master.Delete(ctx, MemberTable, user)
	return err
}

func (s *Store) Kick(ctx context.Context, by, user string) error {
	who, ok1 := s.NetworkOf(by)
	m, ok2 := s.NetworkOf(user)
	if !ok1 || !ok2 || who.Network != m.Network || !canInvite(who) || m.Role == "owner" ||
		(who.Role == "admin" && m.Role == "admin") {
		return ErrForbidden
	}
	_, err := s.Master.Delete(ctx, MemberTable, user)
	return err
}

func (s *Store) SetRole(ctx context.Context, by, user, role string) error {
	who, ok1 := s.NetworkOf(by)
	m, ok2 := s.NetworkOf(user)
	if !ok1 || !ok2 || who.Network != m.Network || who.Role != "owner" || user == by ||
		(role != "admin" && role != "member") {
		return ErrForbidden
	}
	return s.join(ctx, user, m.Network, role)
}

func (s *Store) Transfer(ctx context.Context, by, to string) error {
	who, ok1 := s.NetworkOf(by)
	m, ok2 := s.NetworkOf(to)
	if !ok1 || !ok2 || who.Network != m.Network || who.Role != "owner" || to == by {
		return ErrForbidden
	}
	if err := s.join(ctx, to, m.Network, "owner"); err != nil {
		return err
	}
	if _, err := s.Master.Set(ctx, NetTable, m.Network, map[string]any{"owner": to}); err != nil {
		return err
	}
	return s.join(ctx, by, m.Network, "admin")
}

func (s *Store) DeleteNetwork(ctx context.Context, by string) error {
	who, ok := s.NetworkOf(by)
	if !ok || who.Role != "owner" {
		return ErrForbidden
	}
	for _, m := range s.Members(who.Network) {
		if _, err := s.Master.Delete(ctx, MemberTable, m.User); err != nil {
			return err
		}
	}
	for _, code := range s.State.Keys(InviteTable) {
		if rec, _ := s.State.Get(InviteTable, code); str(rec, "network") == who.Network {
			if _, err := s.Master.Delete(ctx, InviteTable, code); err != nil {
				return err
			}
		}
	}
	_, err := s.Master.Delete(ctx, NetTable, who.Network)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/native/vnet/ -run 'TestNetworkLifecycle|TestOneNetworkPerUserUnderRace' -v`
Expected: PASS (both).

- [ ] **Step 5: Prove the tests bite**

Temporarily change `join` to key memberships by user and network (`s.Master.Set(ctx, MemberTable, user+"@"+network, …)`) and `NetworkOf` to scan for them; run `TestOneNetworkPerUserUnderRace`: Expected FAIL ("c is in 2 networks"). Undo both edits, re-run: PASS.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/vnet/
git add internal/native/vnet/store.go internal/native/vnet/store_test.go
git commit -m "feat(native): users, addresses and virtual networks on the masters

Each user gets a VLESS id, an API token and a permanent 100.64/10 address,
claimed and re-read after replication. Networks, members (one network per
user: the record is keyed by user) and invites live in the op log.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The node spec carries reverse users and the network table

**Files:**
- Modify: `internal/native/nodespec/nodespec.go` (types `Client`, `Inbound`, `Spec`; `Validate`)
- Modify: `internal/native/nodespec/render.go` (`Render`, `Bundle`; export `HopOutbound`)
- Test: `internal/native/nodespec/vnet_test.go`

**Interfaces:**
- Produces (package `nodespec`):
  - `Client.Reverse bool` → rendered `"reverse": {"tag": ReverseTag(email)}`
  - `func ReverseTag(email string) string { return "u-" + email }`
  - `Inbound.VNet bool` — masters add every network user to this inbound
  - `Spec.VNetHop *Hop` — how other nodes reach this node's cascade inbound for network traffic
  - `Spec.VNet *VNet` — filled by the master at render
  - `type VNet struct{ Members []VMember; Peers map[string]Hop; Entry []string; Cascade string }`
  - `type VMember struct{ Email, IP, Network string }`
  - `Bundle.VNet *VNet` (copied from the spec)
  - `func HopOutbound(tag string, h Hop) map[string]any` (the existing `hopOutbound`, exported)
  - policy level 0 gets `"statsUserOnline": true`

- [ ] **Step 1: Write the failing test**

```go
package nodespec

import (
	"encoding/json"
	"strings"
	"testing"
)

const uid = "11111111-1111-4111-8111-111111111111"

func vnetSpec() Spec {
	return Spec{
		Node: "ru4",
		Inbounds: []Inbound{
			{Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws", Route: Direct, VNet: true,
				Clients: []Client{{Email: "alice", ID: uid, Reverse: true}}},
			{Tag: "cascade", Port: 12060, Network: "ws", Path: "/c", Route: Direct,
				Clients: []Client{{Email: "servers", ID: uid}}},
		},
		VNet: &VNet{
			Members: []VMember{{Email: "alice", IP: "100.64.0.7", Network: "n1"}},
			Peers:   map[string]Hop{"de2": {Address: "de2.example", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.example"}},
			Entry:   []string{"vpn"}, Cascade: "cascade",
		},
	}
}

func TestRenderReverseUsersAndOnlineStats(t *testing.T) {
	b, err := Render(vnetSpec())
	if err != nil {
		t.Fatal(err)
	}
	x := string(b.Xray)
	if !strings.Contains(x, `"tag": "u-alice"`) || !strings.Contains(x, `"statsUserOnline": true`) {
		t.Fatalf("xray config lacks the reverse tag or online stats:\n%s", x)
	}
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []map[string]any `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	_ = json.Unmarshal(b.Xray, &cfg)
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "cascade" && strings.Contains(fmtAny(ib.Settings.Clients), "reverse") {
			t.Fatal("a client without Reverse got a reverse tag")
		}
	}
	if b.VNet == nil || b.VNet.Cascade != "cascade" {
		t.Fatalf("bundle lost the network table: %+v", b.VNet)
	}
}

func fmtAny(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestValidateNetworkTable(t *testing.T) {
	for name, change := range map[string]func(*Spec){
		"address outside 100.64/10": func(s *Spec) { s.VNet.Members[0].IP = "10.0.0.7" },
		"unknown cascade inbound":   func(s *Spec) { s.VNet.Cascade = "nope" },
		"unknown entry inbound":     func(s *Spec) { s.VNet.Entry = []string{"nope"} },
		"peer without id":           func(s *Spec) { s.VNet.Peers["de2"] = Hop{Address: "x", Port: 443, Network: "ws", Path: "/c"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := vnetSpec()
			change(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	s := vnetSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/native/nodespec/ -run 'TestRenderReverseUsersAndOnlineStats|TestValidateNetworkTable'`
Expected: FAIL — unknown fields `VNet`, `Reverse`.

- [ ] **Step 3: Implement**

In `nodespec.go`:

```go
type Client struct {
	Email string `json:"email"`
	ID    string `json:"id"`
	// Reverse lets the client take connections from the node (its own tunnel).
	Reverse bool `json:"reverse,omitempty"`
}

// ReverseTag is the node's outbound into a user's reverse tunnel.
func ReverseTag(email string) string { return "u-" + email }

// VNet is the virtual network table a master renders for a node: members,
// how to reach other nodes, which inbounds carry members and cascades.
type VNet struct {
	Members []VMember      `json:"members"`
	Peers   map[string]Hop `json:"peers,omitempty"`
	Entry   []string       `json:"entry"`
	Cascade string         `json:"cascade,omitempty"`
}

type VMember struct {
	Email   string `json:"email"`
	IP      string `json:"ip"`
	Network string `json:"network"`
}

// VNetPrefix holds every member address.
const VNetPrefix = "100.64.0.0/10"
```

Add to `Inbound`: `VNet bool \`json:"vnet,omitempty"\`` (comment: `// VNet: the masters add every network user to this inbound.`)
Add to `Spec`: `VNetHop *Hop \`json:"vnetHop,omitempty"\`` and `VNet *VNet \`json:"vnet,omitempty"\`` (comment: `// VNetHop is how other nodes reach this node's cascade for network traffic; VNet is filled by the master.`)

At the end of `Validate`, before `return nil`:

```go
	if v := s.VNet; v != nil {
		prefix := netip.MustParsePrefix(VNetPrefix)
		for _, m := range v.Members {
			ip, err := netip.ParseAddr(m.IP)
			if m.Email == "" || m.Network == "" || err != nil || !prefix.Contains(ip) {
				return fmt.Errorf("nodespec: bad network member %+v", m)
			}
		}
		for _, tag := range append(slices.Clone(v.Entry), v.Cascade) {
			if tag != "" && !tags[tag] {
				return fmt.Errorf("nodespec: network inbound %q is not an inbound", tag)
			}
		}
		for node, h := range v.Peers {
			if h.Address == "" || h.Port <= 0 || !validUUID.MatchString(h.ID) || checkTransport(h.Network, h.Path) != nil {
				return fmt.Errorf("nodespec: bad network peer %s", node)
			}
		}
	}
```

(add `"net/netip"` to the imports; `tags` already holds every inbound tag at that point).

In `render.go`: the client loop becomes

```go
		for _, c := range ib.Clients {
			client := obj{"id": c.ID, "email": c.Email}
			if c.Reverse {
				client["reverse"] = obj{"tag": ReverseTag(c.Email)}
			}
			clients = append(clients, client)
		}
```

the policy becomes `obj{"levels": obj{"0": obj{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}}`, `Bundle` gets `VNet *VNet \`json:"vnet,omitempty"\``, the return is `Bundle{Xray: raw, Edge: edge, API: api, Certs: s.Certs, Drain: s.Drain, VNet: s.VNet}`, and `hopOutbound` is renamed `HopOutbound` (both call sites).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/native/nodespec/ ./internal/native/xrayrun/`
Expected: PASS (xrayrun's hot-plan tests render specs too).

- [ ] **Step 5: Prove the tests bite** — drop `"statsUserOnline": true` from the policy; `TestRenderReverseUsersAndOnlineStats` FAILS; restore.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/nodespec/
git add internal/native/nodespec/
git commit -m "feat(native): node specs carry reverse users and the network table

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Network routing on a node from the table and presence

**Files:**
- Create: `internal/native/vroute/vroute.go`
- Test: `internal/native/vroute/vroute_test.go`

**Interfaces:**
- Consumes: `nodespec.VNet`, `nodespec.VMember`, `nodespec.ReverseTag`, `nodespec.HopOutbound`, `nodespec.VNetPrefix`, `nodespec.Block`.
- Produces: `func Patch(xray []byte, t *nodespec.VNet, where map[string]string, self string) ([]byte, error)` — `where` maps an email to the node it is online on; returns xray with network rules placed right after the api rule, `vnet-<node>` outbounds for peers in use, and a final `100.64.0.0/10 → block`. `t == nil` returns xray unchanged.
- Rule order, per member B (address `ipB`, network N):
  1. B online here: `{inboundTag: t.Entry, user: emails of N, ip: [ipB]} → u-B`, and `{inboundTag: [t.Cascade], ip: [ipB]} → u-B`
  2. B online on peer Y: `{inboundTag: t.Entry, user: emails of N, ip: [ipB]} → vnet-Y`
  3. otherwise nothing for B
  then `{ip: [100.64.0.0/10]} → block`.

- [ ] **Step 1: Write the failing test**

```go
package vroute_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vroute"
)

const uid = "11111111-1111-4111-8111-111111111111"

type rule struct {
	InboundTag  []string `json:"inboundTag"`
	User        []string `json:"user"`
	IP          []string `json:"ip"`
	OutboundTag string   `json:"outboundTag"`
}

func base(t *testing.T) []byte {
	t.Helper()
	b, err := nodespec.Render(nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{
		{Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct},
		{Tag: "cascade", Port: 2, Network: "ws", Path: "/c", Route: nodespec.Direct},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b.Xray
}

func table() *nodespec.VNet {
	return &nodespec.VNet{
		Members: []nodespec.VMember{
			{Email: "alice", IP: "100.64.0.1", Network: "n1"},
			{Email: "bob", IP: "100.64.0.2", Network: "n1"},
			{Email: "eve", IP: "100.64.0.3", Network: "n2"},
		},
		Peers: map[string]nodespec.Hop{"de2": {Address: "de2.x", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.x"}},
		Entry: []string{"vpn"}, Cascade: "cascade",
	}
}

func parse(t *testing.T, x []byte) ([]rule, []string) {
	t.Helper()
	var cfg struct {
		Routing   struct{ Rules []rule } `json:"routing"`
		Outbounds []struct{ Tag string } `json:"outbounds"`
	}
	if err := json.Unmarshal(x, &cfg); err != nil {
		t.Fatal(err)
	}
	var outs []string
	for _, o := range cfg.Outbounds {
		outs = append(outs, o.Tag)
	}
	return cfg.Routing.Rules, outs
}

func to(rules []rule, ip, inbound string) string {
	for _, r := range rules {
		if slices.Contains(r.IP, ip) && (len(r.InboundTag) == 0 || slices.Contains(r.InboundTag, inbound)) {
			return r.OutboundTag
		}
	}
	return ""
}

func TestPatchRoutesMembers(t *testing.T) {
	x, err := vroute.Patch(base(t), table(), map[string]string{"alice": "ru4", "bob": "de2", "eve": "ru4"}, "ru4")
	if err != nil {
		t.Fatal(err)
	}
	rules, outs := parse(t, x)
	if got := to(rules, "100.64.0.1", "vpn"); got != "u-alice" {
		t.Fatalf("alice (here) routed to %q", got)
	}
	if got := to(rules, "100.64.0.1", "cascade"); got != "u-alice" {
		t.Fatalf("cascade delivery to alice: %q", got)
	}
	if got := to(rules, "100.64.0.2", "vpn"); got != "vnet-de2" || !slices.Contains(outs, "vnet-de2") {
		t.Fatalf("bob (on de2) routed to %q, outbounds %v", got, outs)
	}
	for _, r := range rules {
		if slices.Contains(r.IP, "100.64.0.1") && slices.Contains(r.User, "eve") {
			t.Fatal("eve (another network) may reach alice")
		}
	}
	if last := rules[len(rules)-1]; !slices.Contains(last.IP, nodespec.VNetPrefix) || last.OutboundTag != nodespec.Block {
		t.Fatalf("no final block for the network range: %+v", last)
	}
	if rules[0].OutboundTag != "api" {
		t.Fatal("the api rule is no longer first")
	}
}

// Review focus 1 and 3: without presence, or with the user newest on
// another node, nothing routes into this node's (possibly stale) tunnel.
func TestPatchIgnoresStaleTunnels(t *testing.T) {
	x, _ := vroute.Patch(base(t), table(), map[string]string{"bob": "de2"}, "ru4")
	rules, _ := parse(t, x)
	if got := to(rules, "100.64.0.1", "vpn"); got != nodespec.Block {
		t.Fatalf("alice offline, still routed to %q", got)
	}
	if got := to(rules, "100.64.0.2", "cascade"); got != nodespec.Block {
		t.Fatalf("cascade traffic for bob (on de2) delivered here: %q", got)
	}
}

// Review focus 5: a member who left is not in the table and reaches nobody.
func TestPatchDropsFormerMembers(t *testing.T) {
	tb := table()
	tb.Members = tb.Members[:1] // bob left
	x, _ := vroute.Patch(base(t), tb, map[string]string{"alice": "ru4", "bob": "ru4"}, "ru4")
	rules, _ := parse(t, x)
	for _, r := range rules {
		if slices.Contains(r.User, "bob") {
			t.Fatalf("bob still in a rule: %+v", r)
		}
	}
}

func TestPatchWithoutTableKeepsConfig(t *testing.T) {
	in := base(t)
	out, err := vroute.Patch(in, nil, nil, "ru4")
	if err != nil || string(out) != string(in) {
		t.Fatal("nil table changed the config")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/native/vroute/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package vroute turns a node's network table and the fleet's presence into
// Xray routing: members reach members of their network, on this node through
// their reverse tunnel, elsewhere over a cascade.
package vroute

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

type obj = map[string]any

func PeerTag(node string) string { return "vnet-" + node }

// Patch places the network rules right after the api rule, adds the
// cascades it uses, and blocks the rest of the network range.
func Patch(xray []byte, t *nodespec.VNet, where map[string]string, self string) ([]byte, error) {
	if t == nil {
		return xray, nil
	}
	var cfg obj
	if err := json.Unmarshal(xray, &cfg); err != nil {
		return nil, err
	}
	byNet := map[string][]string{}
	for _, m := range t.Members {
		byNet[m.Network] = append(byNet[m.Network], m.Email)
	}
	var rules []any
	peers := map[string]bool{}
	for _, m := range t.Members {
		users := slices.Sorted(slices.Values(byNet[m.Network]))
		switch node := where[m.Email]; {
		case node == self:
			rules = append(rules,
				obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(m.Email)})
			if t.Cascade != "" {
				rules = append(rules,
					obj{"type": "field", "inboundTag": []string{t.Cascade}, "ip": []string{m.IP}, "outboundTag": nodespec.ReverseTag(m.Email)})
			}
		case node != "":
			if _, ok := t.Peers[node]; ok {
				peers[node] = true
				rules = append(rules,
					obj{"type": "field", "inboundTag": t.Entry, "user": users, "ip": []string{m.IP}, "outboundTag": PeerTag(node)})
			}
		}
	}
	rules = append(rules, obj{"type": "field", "ip": []string{nodespec.VNetPrefix}, "outboundTag": nodespec.Block})

	routing, _ := cfg["routing"].(obj)
	old, _ := routing["rules"].([]any)
	merged := append([]any{}, old[:min(1, len(old))]...) // the api rule stays first
	merged = append(merged, rules...)
	merged = append(merged, old[min(1, len(old)):]...)
	routing["rules"] = merged

	outs, _ := cfg["outbounds"].([]any)
	for _, node := range slices.Sorted(maps.Keys(peers)) {
		outs = append(outs, nodespec.HopOutbound(PeerTag(node), t.Peers[node]))
	}
	cfg["outbounds"] = outs
	return json.MarshalIndent(cfg, "", "  ")
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/native/vroute/ -v`; Expected: PASS (4 tests).

- [ ] **Step 5: Prove the tests bite** — change `case node == self:` to `case node == self || node == "":`; `TestPatchIgnoresStaleTunnels` FAILS; restore.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/vroute/
git add internal/native/vroute/
git commit -m "feat(native): route network members from the table and presence

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Presence on the bus

**Files:**
- Create: `internal/native/presence/presence.go`
- Test: `internal/native/presence/presence_test.go`
- Modify: `internal/native/bus/bus.go` (`nodeAuth.Check` permissions)
- Modify: `internal/native/bus/auth_test.go` (a presence case)

**Interfaces:**
- Produces (package `presence`):
  - `func Subject(node string) string { return "presence." + node }`
  - `type Report struct{ Node string; Users []string; At int64 }` (JSON `n`, `u`, `at`)
  - `func Publish(nc *nats.Conn, node string, users []string, at time.Time) error`
  - `type Map struct{ TTL time.Duration; Now func() time.Time; … }`
  - `(*Map) Watch(nc *nats.Conn) (*nats.Subscription, error)`, `(*Map) Add(r Report)`, `(*Map) Where() map[string]string`, `(*Map) Changed() <-chan struct{}` (a signal, capacity 1)
- Bus: a node may publish `presence.<itself>` and subscribe `presence.>`.

- [ ] **Step 1: Write the failing tests**

`internal/native/presence/presence_test.go`:

```go
package presence_test

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

func TestWhereNewestWinsAndEntriesExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	m := &presence.Map{TTL: 10 * time.Second, Now: func() time.Time { return now }}
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice", "bob"}, At: now.UnixMilli()})
	m.Add(presence.Report{Node: "de2", Users: []string{"alice"}, At: now.Add(time.Second).UnixMilli()})
	w := m.Where()
	if w["alice"] != "de2" || w["bob"] != "ru4" {
		t.Fatalf("where = %v", w)
	}
	select {
	case <-m.Changed():
	default:
		t.Fatal("no change signalled")
	}
	// ru4 keeps reporting alice, but de2's newer report still wins
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice", "bob"}, At: now.Add(500 * time.Millisecond).UnixMilli()})
	if m.Where()["alice"] != "de2" {
		t.Fatal("an older report took alice back")
	}
	// review focus 2: de2 dies silently; after TTL alice falls back to ru4's report
	now = now.Add(11 * time.Second)
	m.Add(presence.Report{Node: "ru4", Users: []string{"alice"}, At: now.UnixMilli()})
	if w := m.Where(); w["alice"] != "ru4" || w["bob"] != "" {
		t.Fatalf("after de2 went quiet: %v", w)
	}
}
```

In `internal/native/bus/auth_test.go`, append to `TestNodesAuthenticateByFleetCert` right after the probe check:

```go
	if err := nc.Publish(presence.Subject("nl2"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil || nc.LastError() != nil {
		t.Fatalf("own presence refused: %v %v", err, nc.LastError())
	}
	sub, err := nc.SubscribeSync(presence.Subject("*"))
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()
	if nc.LastError() != nil {
		t.Fatalf("presence subscription refused: %v", nc.LastError())
	}
	_ = sub.Unsubscribe()
	_ = nc.Publish(presence.Subject("de9"), []byte(`{}`))
	_ = nc.Flush()
	time.Sleep(200 * time.Millisecond)
	if nc.LastError() == nil {
		t.Fatal("nl2 published presence as de9")
	}
```

(import `"github.com/mhsanaei/3x-ui/v3/internal/native/presence"`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/native/presence/ ./internal/native/bus/ -run 'TestWhere|TestNodesAuthenticate'`
Expected: FAIL — package `presence` missing.

- [ ] **Step 3: Implement**

`internal/native/presence/presence.go`:

```go
// Package presence tells the fleet who is online where: every node
// publishes its online users, every node and master keeps the newest.
package presence

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

func Subject(node string) string { return "presence." + node }

type Report struct {
	Node  string   `json:"n"`
	Users []string `json:"u"`
	At    int64    `json:"at"` // unix ms
}

func Publish(nc *nats.Conn, node string, users []string, at time.Time) error {
	data, err := json.Marshal(Report{Node: node, Users: users, At: at.UnixMilli()})
	if err != nil {
		return err
	}
	return nc.Publish(Subject(node), data)
}

type seen struct {
	node string
	at   int64
}

// Map is the fleet's presence; a node silent for TTL is forgotten.
type Map struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	reports map[string]Report // by node
	last    map[string]string
	changed chan struct{}
}

func (m *Map) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Map) init() {
	if m.reports == nil {
		m.reports = map[string]Report{}
		m.changed = make(chan struct{}, 1)
	}
}

func (m *Map) Add(r Report) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if old, ok := m.reports[r.Node]; ok && old.At > r.At {
		return
	}
	m.reports[r.Node] = r
	if w := m.whereLocked(); !sameMap(w, m.last) {
		m.last = w
		select {
		case m.changed <- struct{}{}:
		default:
		}
	}
}

// Where maps each online user to the node with its newest report.
func (m *Map) Where() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.whereLocked()
}

func (m *Map) whereLocked() map[string]string {
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	cutoff := m.now().Add(-ttl).UnixMilli()
	best := map[string]seen{}
	for node, r := range m.reports {
		if r.At < cutoff {
			continue
		}
		for _, u := range r.Users {
			if b, ok := best[u]; !ok || r.At > b.at {
				best[u] = seen{node, r.At}
			}
		}
	}
	out := make(map[string]string, len(best))
	for u, s := range best {
		out[u] = s.node
	}
	return out
}

func (m *Map) Changed() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.changed
}

// Watch feeds the map from every node's reports.
func (m *Map) Watch(nc *nats.Conn) (*nats.Subscription, error) {
	return nc.Subscribe(Subject("*"), func(msg *nats.Msg) {
		var r Report
		if json.Unmarshal(msg.Data, &r) == nil && r.Node != "" && Subject(r.Node) == msg.Subject {
			m.Add(r)
		}
	})
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
```

In `bus.go` `nodeAuth.Check`, the node's permissions become:

```go
		Publish:   &server.SubjectPermission{Allow: []string{"rep.*." + node, "probe.*", wire.DesiredSubject("*", node), "presence." + node}},
		Subscribe: &server.SubjectPermission{Allow: []string{wire.NodeInbox(node) + ".>", "presence.*"}},
```

- [ ] **Step 4: Run tests** — `go test ./internal/native/presence/ ./internal/native/bus/`; Expected: PASS.

- [ ] **Step 5: Prove the tests bite** — make `whereLocked` skip the `cutoff` check; `TestWhereNewestWinsAndEntriesExpire` FAILS; restore. Remove `"presence." + node` from Publish permissions; the auth test FAILS; restore.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/presence/ ./internal/native/bus/
git add internal/native/presence/ internal/native/bus/bus.go internal/native/bus/auth_test.go
git commit -m "feat(native): nodes share who is online over the bus

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The agent publishes presence and routes the network live

**Files:**
- Modify: `internal/native/xrayrun/xrayrun.go` (add `Online`)
- Test: `internal/native/xrayrun/xrayrun_test.go` (parser test)
- Modify: `internal/native/agent/bundle.go` (`BundleApplier` patches with presence)
- Create: `internal/native/agent/presence.go` (publish loop and re-apply on change)
- Test: `internal/native/agent/presence_test.go`
- Modify: `cmd/fbnode/main.go` (wire it)

**Interfaces:**
- Consumes: `vroute.Patch`, `presence.Map`, `presence.Publish`, `nodespec.Bundle.VNet`.
- Produces:
  - `func (r *Runner) Online(ctx context.Context) ([]string, error)` — emails from `xray api statsgetallonlineusers`
  - `func parseOnline(out []byte) ([]string, error)` (package-private, tested)
  - `BundleApplier{Xray, Edge, Node string, Presence *presence.Map}`; `(*BundleApplier) Apply(ctx, st)` patches `bundle.Xray` with `vroute.Patch(bundle.Xray, bundle.VNet, Presence.Where(), Node)`; `(*BundleApplier) Repatch(ctx) error` re-applies the last bundle with fresh presence
  - `type Presence struct{ Node string; Xray interface{ Online(context.Context) ([]string, error) }; Map *presence.Map; Applier *BundleApplier; Conn func(ctx context.Context) (*nats.Conn, error); Every time.Duration; Log func(string, ...any) }`, `(*Presence) Run(ctx)`

- [ ] **Step 1: Write the failing tests**

In `xrayrun_test.go`:

```go
func TestParseOnline(t *testing.T) {
	got, err := parseOnline([]byte(`{ "users": [ "user>>>alice>>>online", "user>>>bob>>>online" ] }`))
	if err != nil || !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := parseOnline([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}
```

`internal/native/agent/presence_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

type fakeOnline []string

func (f fakeOnline) Online(context.Context) ([]string, error) { return f, nil }

// A presence change re-applies the bundle; the same presence does not.
func TestPresenceChangeRepatches(t *testing.T) {
	m := &presence.Map{}
	applied := 0
	p := &Presence{Node: "ru4", Map: m, Every: 20 * time.Millisecond, repatch: func(context.Context) error { applied++; return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go p.watch(ctx)
	m.Add(presence.Report{Node: "de2", Users: []string{"bob"}, At: time.Now().UnixMilli()})
	time.Sleep(100 * time.Millisecond)
	m.Add(presence.Report{Node: "de2", Users: []string{"bob"}, At: time.Now().UnixMilli()})
	<-ctx.Done()
	if applied != 1 {
		t.Fatalf("repatched %d times, want 1", applied)
	}
}
```

- [ ] **Step 2: Run to verify they fail** — `go test ./internal/native/xrayrun/ ./internal/native/agent/`; Expected: FAIL (undefined `parseOnline`, `Presence`).

- [ ] **Step 3: Implement**

`xrayrun.go`:

```go
// Online lists the users with a live connection (policy statsUserOnline).
func (r *Runner) Online(ctx context.Context) ([]string, error) {
	if r.API == "" {
		return nil, errors.New("xrayrun: no API address")
	}
	out, err := exec.CommandContext(ctx, r.Bin, "api", "statsgetallonlineusers", "--server="+r.API).Output()
	if err != nil {
		return nil, fmt.Errorf("xrayrun: online: %w", err)
	}
	return parseOnline(out)
}

func parseOnline(out []byte) ([]string, error) {
	var res struct {
		Users []string `json:"users"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("xrayrun: online: %w", err)
	}
	var emails []string
	for _, u := range res.Users {
		if parts := strings.Split(u, ">>>"); len(parts) == 3 {
			emails = append(emails, parts[1])
		}
	}
	return emails, nil
}
```

`agent/bundle.go`:

```go
type BundleApplier struct {
	Xray     *xrayrun.Runner
	Edge     *edgerun.Runner
	Node     string
	Presence *presence.Map // nil: no network routing

	mu   sync.Mutex
	last *nodespec.Bundle
}

func (b *BundleApplier) Apply(ctx context.Context, st desired.State) error {
	var bundle nodespec.Bundle
	if err := json.Unmarshal(st.Config, &bundle); err != nil {
		return fmt.Errorf("agent: bundle: %w", err)
	}
	if err := b.applyXray(ctx, &bundle); err != nil {
		return err
	}
	if err := b.Edge.Apply(bundle.Edge, bundle.Certs); err != nil {
		return err
	}
	return b.Edge.SetDrain(bundle.Drain)
}

func (b *BundleApplier) applyXray(ctx context.Context, bundle *nodespec.Bundle) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var where map[string]string
	if b.Presence != nil {
		where = b.Presence.Where()
	}
	x, err := vroute.Patch(bundle.Xray, bundle.VNet, where, b.Node)
	if err != nil {
		return err
	}
	b.Xray.API = bundle.API
	if err := b.Xray.Apply(ctx, x); err != nil {
		return err
	}
	b.last = bundle
	return nil
}

// Repatch applies the last bundle again with the fleet's current presence.
func (b *BundleApplier) Repatch(ctx context.Context) error {
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	if last == nil || last.VNet == nil {
		return nil
	}
	return b.applyXray(ctx, last)
}
```

`agent/presence.go`:

```go
package agent

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

// Presence publishes this node's online users and re-routes the network
// whenever the fleet's presence changes.
type Presence struct {
	Node  string
	Xray  interface{ Online(context.Context) ([]string, error) }
	Map   *presence.Map
	Conn  func(ctx context.Context) (*nats.Conn, error)
	Every time.Duration // default 2 s
	Log   func(format string, args ...any)

	repatch func(context.Context) error
	watched *nats.Conn
}

func NewPresence(node string, x interface{ Online(context.Context) ([]string, error) }, m *presence.Map,
	a *BundleApplier, conn func(ctx context.Context) (*nats.Conn, error), log func(string, ...any)) *Presence {
	return &Presence{Node: node, Xray: x, Map: m, Conn: conn, Log: log, repatch: a.Repatch}
}

func (p *Presence) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func (p *Presence) Run(ctx context.Context) {
	go p.watch(ctx)
	t := time.NewTicker(orDefault(p.Every, 2*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.publish(ctx)
		}
	}
}

func (p *Presence) publish(ctx context.Context) {
	users, err := p.Xray.Online(ctx)
	if err != nil {
		return // no Xray yet
	}
	nc, err := p.Conn(ctx)
	if err != nil {
		return
	}
	if nc != p.watched { // a new master: subscribe there
		if _, err := p.Map.Watch(nc); err != nil {
			p.logf("presence: %v", err)
			return
		}
		p.watched = nc
	}
	if err := presence.Publish(nc, p.Node, users, time.Now()); err != nil {
		p.logf("presence: %v", err)
	}
}

func (p *Presence) watch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.Map.Changed():
			if err := p.repatch(ctx); err != nil {
				p.logf("network routes: %v", err)
			}
		}
	}
}
```

`cmd/fbnode/main.go` — after `bundles := …` set `bundles.Node = c.Node; pres := &presence.Map{}; bundles.Presence = pres`; after `node := &agent.Node{…}`:

```go
	pr := agent.NewPresence(c.Node, xr, pres, bundles, func(ctx context.Context) (*nats.Conn, error) {
		m := sel.Current()
		if m == "" {
			return nil, errors.New("no master yet")
		}
		return node.Sender.Conn(ctx, m)
	}, logger.Printf)
```

and before `node.Run(ctx)`: `go pr.Run(ctx)`.

- [ ] **Step 4: Run tests** — `go test ./internal/native/xrayrun/ ./internal/native/agent/ && go build ./cmd/fbnode`; Expected: PASS, builds.

- [ ] **Step 5: Prove the tests bite** — in `watch`, drop the `Changed()` wait and repatch every tick; `TestPresenceChangeRepatches` FAILS; restore.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/xrayrun/ ./internal/native/agent/ ./cmd/fbnode/
git add internal/native/xrayrun internal/native/agent cmd/fbnode
git commit -m "feat(native): nodes publish presence and re-route the network live

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Masters render the network and serve the API

**Files:**
- Modify: `internal/native/masterd/masterd.go` (render fills network users and table; admin `PUT /users/{id}`; mount `/api/v1`; presence watch)
- Create: `internal/native/vnet/api.go`
- Test: `internal/native/vnet/api_test.go`
- Create: `internal/native/vnet/render.go`
- Test: `internal/native/vnet/render_test.go`

**Interfaces:**
- Consumes: Task 1 `Store`; `nodespec.Spec{VNetHop, VNet, Inbound.VNet}`; `presence.Map`.
- Produces:
  - `func (s *Store) Fill(spec *nodespec.Spec, specs map[string]nodespec.Spec)` — adds every user (with `Reverse: true`) to inbounds with `VNet: true`; sets `spec.VNet` = members of all networks, `Peers` = every other node's `VNetHop`, `Entry` = tags of `VNet` inbounds, `Cascade` = the inbound tag whose clients include `servers` (the cascade convention of the lab), empty if none
  - `func API(s *Store, where func() map[string]string) http.Handler` — mounted at `/api/v1/`, bearer token = user token
  - masterd: `PUT /users/{id}` (admin) → `vnet.User` JSON; `Config` unchanged

- [ ] **Step 1: Write the failing tests**

`render_test.go` (uses `fleet`/`until` from `store_test.go`):

```go
package vnet_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

const uid = "11111111-1111-4111-8111-111111111111"

func TestFillAddsUsersAndTable(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	s := st["fi1"]
	a, _ := s.CreateUser(ctx, "alice")
	_, _ = s.CreateUser(ctx, "bob")
	n, _ := s.CreateNetwork(ctx, "alice", "x")
	code, _ := s.Invite(ctx, "alice", time.Hour, 1)
	_ = s.Join(ctx, "bob", code)
	ru4 := nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{
		{Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct, VNet: true},
		{Tag: "cascade", Port: 2, Network: "ws", Path: "/c", Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: uid}}},
	}}
	de2 := nodespec.Spec{Node: "de2", VNetHop: &nodespec.Hop{Address: "de2.x", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.x"}}
	s.Fill(&ru4, map[string]nodespec.Spec{"ru4": ru4, "de2": de2})

	var alice nodespec.Client
	for _, c := range ru4.Inbounds[0].Clients {
		if c.Email == "alice" {
			alice = c
		}
	}
	if alice.ID != a.UUID || !alice.Reverse || len(ru4.Inbounds[0].Clients) != 2 {
		t.Fatalf("vpn clients %+v", ru4.Inbounds[0].Clients)
	}
	v := ru4.VNet
	if v == nil || v.Cascade != "cascade" || !slices.Equal(v.Entry, []string{"vpn"}) || v.Peers["de2"].Address != "de2.x" {
		t.Fatalf("table %+v", v)
	}
	if len(v.Members) != 2 || v.Members[0].Network != n {
		t.Fatalf("members %+v", v.Members)
	}
	if _, err := nodespec.Render(ru4); err != nil {
		t.Fatalf("filled spec does not render: %v", err)
	}
}
```

`api_test.go`:

```go
package vnet_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func call(t *testing.T, h http.Handler, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAPIFlow(t *testing.T) {
	ctx := context.Background()
	st := fleet(t)
	s := st["fi1"]
	a, _ := s.CreateUser(ctx, "alice")
	b, _ := s.CreateUser(ctx, "bob")
	h := vnet.API(s, func() map[string]string { return map[string]string{"alice": "ru4"} })

	if code, _ := call(t, h, "", "GET", "/api/v1/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, me := call(t, h, a.Token, "GET", "/api/v1/me", ""); code != 200 || me["ip"] != a.IP {
		t.Fatalf("me: %d %v", code, me)
	}
	if code, _ := call(t, h, a.Token, "POST", "/api/v1/network", `{"title":"друзья"}`); code != 200 {
		t.Fatalf("create: %d", code)
	}
	code, inv := call(t, h, a.Token, "POST", "/api/v1/network/invites", `{"ttl":"1h","uses":1}`)
	if code != 200 || inv["code"] == "" {
		t.Fatalf("invite: %d %v", code, inv)
	}
	if code, _ := call(t, h, b.Token, "POST", "/api/v1/network/join", `{"code":"`+inv["code"].(string)+`"}`); code != 200 {
		t.Fatalf("join: %d", code)
	}
	code, net := call(t, h, b.Token, "GET", "/api/v1/network", "")
	members, _ := net["members"].([]any)
	if code != 200 || len(members) != 2 {
		t.Fatalf("network: %d %v", code, net)
	}
	online := 0
	for _, m := range members {
		if m.(map[string]any)["node"] == "ru4" {
			online++
		}
	}
	if online != 1 {
		t.Fatalf("presence not shown: %v", members)
	}
	if code, e := call(t, h, b.Token, "POST", "/api/v1/network", `{"title":"вторая"}`); code != http.StatusConflict || e["error"] != vnet.ErrInNetwork.Error() {
		t.Fatalf("second network: %d %v", code, e)
	}
}
```

- [ ] **Step 2: Run to verify they fail** — `go test ./internal/native/vnet/`; Expected: FAIL (undefined `Fill`, `API`).

- [ ] **Step 3: Implement**

`render.go`:

```go
package vnet

import (
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// CascadeUser is the client other nodes use on a node's cascade inbound.
const CascadeUser = "servers"

// Fill adds every network user to the spec's network inbounds and sets the
// table: members of every network and how to reach every other node.
func (s *Store) Fill(spec *nodespec.Spec, specs map[string]nodespec.Spec) {
	users := s.Users()
	t := &nodespec.VNet{Peers: map[string]nodespec.Hop{}}
	for i := range spec.Inbounds {
		ib := &spec.Inbounds[i]
		if ib.VNet {
			t.Entry = append(t.Entry, ib.Tag)
			for _, u := range users {
				ib.Clients = append(ib.Clients, nodespec.Client{Email: u.Email, ID: u.UUID, Reverse: true})
			}
		}
		if slices.ContainsFunc(ib.Clients, func(c nodespec.Client) bool { return c.Email == CascadeUser }) {
			t.Cascade = ib.Tag
		}
	}
	if len(t.Entry) == 0 {
		return
	}
	for _, u := range users {
		if m, ok := s.NetworkOf(u.ID); ok {
			t.Members = append(t.Members, nodespec.VMember{Email: u.Email, IP: u.IP, Network: m.Network})
		}
	}
	for node, other := range specs {
		if node != spec.Node && other.VNetHop != nil {
			t.Peers[node] = *other.VNetHop
		}
	}
	spec.VNet = t
}
```

`api.go`:

```go
package vnet

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type ctxUser struct{}

// API is the users' side, under /api/v1/, authenticated by the user's token.
func API(s *Store, where func() map[string]string) http.Handler {
	mux := http.NewServeMux()
	auth := func(h func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			u, ok := s.UserByToken(token)
			if !ok {
				fail(w, http.StatusUnauthorized, errors.New("нужен токен"))
				return
			}
			h(w, r, u)
		}
	}
	mux.HandleFunc("GET /api/v1/me", auth(func(w http.ResponseWriter, _ *http.Request, u User) {
		m, _ := s.NetworkOf(u.ID)
		ok(w, map[string]any{"id": u.ID, "ip": u.IP, "network": m.Network, "role": m.Role, "node": where()[u.Email]})
	}))
	mux.HandleFunc("POST /api/v1/network", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, err := s.CreateNetwork(r.Context(), u.ID, body.Title)
		reply(w, map[string]any{"network": id}, err)
	}))
	mux.HandleFunc("GET /api/v1/network", auth(func(w http.ResponseWriter, _ *http.Request, u User) {
		m, in := s.NetworkOf(u.ID)
		if !in {
			fail(w, http.StatusNotFound, ErrNoNetwork)
			return
		}
		online := where()
		var members []map[string]any
		for _, mm := range s.Members(m.Network) {
			peer, _ := s.User(mm.User)
			members = append(members, map[string]any{"id": mm.User, "ip": peer.IP, "role": mm.Role, "node": online[peer.Email]})
		}
		ok(w, map[string]any{"network": m.Network, "role": m.Role, "members": members})
	}))
	mux.HandleFunc("DELETE /api/v1/network", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.DeleteNetwork(r.Context(), u.ID))
	}))
	mux.HandleFunc("POST /api/v1/network/leave", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Leave(r.Context(), u.ID))
	}))
	mux.HandleFunc("POST /api/v1/network/transfer", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ To string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.Transfer(r.Context(), u.ID, body.To))
	}))
	mux.HandleFunc("POST /api/v1/network/invites", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct {
			TTL  string
			Uses int
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ttl, err := time.ParseDuration(body.TTL)
		if err != nil || ttl <= 0 {
			ttl = 24 * time.Hour
		}
		code, err := s.Invite(r.Context(), u.ID, ttl, body.Uses)
		reply(w, map[string]any{"code": code}, err)
	}))
	mux.HandleFunc("DELETE /api/v1/network/invites/{code}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Revoke(r.Context(), u.ID, r.PathValue("code")))
	}))
	mux.HandleFunc("POST /api/v1/network/join", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.Join(r.Context(), u.ID, body.Code))
	}))
	mux.HandleFunc("DELETE /api/v1/network/members/{user}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Kick(r.Context(), u.ID, r.PathValue("user")))
	}))
	mux.HandleFunc("PATCH /api/v1/network/members/{user}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Role string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.SetRole(r.Context(), u.ID, r.PathValue("user"), body.Role))
	}))
	return mux
}

func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		ok(w, v)
	case errors.Is(err, ErrInNetwork), errors.Is(err, ErrOwnerLeaves):
		fail(w, http.StatusConflict, err)
	case errors.Is(err, ErrForbidden):
		fail(w, http.StatusForbidden, err)
	case errors.Is(err, ErrNoNetwork), errors.Is(err, ErrNoUser), errors.Is(err, ErrBadInvite):
		fail(w, http.StatusNotFound, err)
	default:
		fail(w, http.StatusInternalServerError, err)
	}
}
```

`masterd.go`:
- `Daemon` gets `vnet atomic.Pointer[vnet.Store]` and `presence *presence.Map` (created in `Start`: `d.presence = &presence.Map{}`).
- In `boot`, after `d.m.Store(m)`: `d.vnet.Store(&vnet.Store{Master: m, State: d.store.State})` and `if _, err := d.presence.Watch(d.bus.Conn()); err != nil { d.logf("presence: %v", err) }`.
- In `render`, after unmarshalling `spec` and before resolving certificates:

```go
		if s := d.vnet.Load(); s != nil {
			specs := map[string]nodespec.Spec{}
			for _, other := range d.store.State.Keys(SpecTable) {
				rec, _ := d.store.State.Get(SpecTable, other)
				var o nodespec.Spec
				if json.Unmarshal(rec["spec"], &o) == nil {
					specs[other] = o
				}
			}
			s.Fill(&spec, specs)
		}
```

- In `Handler()`:

```go
	mux.HandleFunc("PUT /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := d.vnet.Load()
		if s == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		u, err := s.CreateUser(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(u)
	})
	mux.Handle("/api/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := d.vnet.Load()
		if s == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		vnet.API(s, d.presence.Where).ServeHTTP(w, r)
	}))
```

and in `Public()` the same `/api/v1/` handler, so it is served on `master.<zone>` once the pool is up.

- [ ] **Step 4: Run tests** — `go test ./internal/native/vnet/ ./internal/native/masterd/ && go build ./internal/native/...`; Expected: PASS.

- [ ] **Step 5: Prove the tests bite** — in `Fill`, drop `Reverse: true`; `TestFillAddsUsersAndTable` FAILS; restore.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/vnet/ ./internal/native/masterd/
git add internal/native/vnet internal/native/masterd
git commit -m "feat(native): masters render the network and serve /api/v1

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The client's Xray config for a network member

**Files:**
- Create: `internal/native/vnet/client.go`
- Test: `internal/native/vnet/client_test.go`

**Interfaces:**
- Produces: `type ClientEntry struct{ Address string; Port int; SNI, Host, Network, Path string }`; `func ClientConfig(u User, e ClientEntry, inbounds []map[string]any) ([]byte, error)` — outbounds `vpn` (VLESS to the entry with `"reverse": {"tag": "from-node"}`) and `local` (`freedom`, `redirect: "127.0.0.1:0"`, `finalRules` allowing `127.0.0.0/8`); rules: `from-node → local`, `100.64.0.0/10 → vpn`, the rest `→ vpn`. `inbounds` are the device's own (socks in the lab, tun in apps).

- [ ] **Step 1: Write the failing test**

```go
package vnet_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func TestClientConfigIsValidXray(t *testing.T) {
	x := os.Getenv("NETLAB_XRAY")
	if x == "" {
		x = "/usr/local/x-ui/bin/xray-linux-amd64"
	}
	if _, err := os.Stat(x); err != nil {
		t.Skip("no xray binary")
	}
	cfg, err := vnet.ClientConfig(
		vnet.User{Email: "alice", UUID: "11111111-1111-4111-8111-111111111111", IP: "100.64.0.7"},
		vnet.ClientEntry{Address: "ru.example", Port: 443, SNI: "ru.example", Host: "ru.example", Network: "ws", Path: "/ws"},
		[]map[string]any{{"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"from-node"`, `"127.0.0.1:0"`, `"100.64.0.0/10"`} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config lacks %s:\n%s", want, cfg)
		}
	}
	f := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(f, cfg, 0o600)
	if out, err := exec.Command(x, "run", "-test", "-c", f).CombinedOutput(); err != nil {
		t.Fatalf("xray rejects it: %s", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/native/vnet/ -run TestClientConfigIsValidXray`; Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

```go
package vnet

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// ClientEntry is the node (or group name) the client connects to.
type ClientEntry struct {
	Address       string
	Port          int
	SNI, Host     string
	Network, Path string
}

// ClientConfig is a member's Xray config: its tunnel carries the network
// both ways; what the node sends back lands on the device's own ports.
func ClientConfig(u User, e ClientEntry, inbounds []map[string]any) ([]byte, error) {
	hop := nodespec.HopOutbound("vpn", nodespec.Hop{
		Address: e.Address, Port: e.Port, ID: u.UUID, Network: e.Network, Path: e.Path, SNI: e.SNI, Host: e.Host,
	})
	// the flat VLESS form is the one the lab's reverse test proved on Xray 26
	vpn := map[string]any{
		"tag": "vpn", "protocol": "vless", "streamSettings": hop["streamSettings"],
		"settings": map[string]any{
			"address": e.Address, "port": e.Port, "id": u.UUID, "email": u.Email, "encryption": "none",
			"reverse": map[string]any{"tag": "from-node"},
		},
	}
	local := map[string]any{
		"tag": "local", "protocol": "freedom",
		"settings": map[string]any{
			"redirect":   "127.0.0.1:0",
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}},
		},
	}
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": []any{vpn, local},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"from-node"}, "outboundTag": "local"},
			map[string]any{"type": "field", "ip": []string{nodespec.VNetPrefix}, "outboundTag": "vpn"},
		}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/native/vnet/ -run TestClientConfigIsValidXray -v`; Expected: PASS.

- [ ] **Step 5: Prove the test bites** — change `"from-node"` in the routing rule only (not in `reverse`) to `"from-nodes"`; re-run: FAIL on the rule check below; restore.

Add this check to the test before writing the file (it is what catches the mismatch; the `strings.Contains` checks alone would not):

```go
	var parsed struct {
		Outbounds []struct {
			Settings struct {
				Reverse struct{ Tag string } `json:"reverse"`
			} `json:"settings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag []string `json:"inboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	_ = json.Unmarshal(cfg, &parsed)
	if tag := parsed.Outbounds[0].Settings.Reverse.Tag; tag == "" || parsed.Routing.Rules[0].InboundTag[0] != tag {
		t.Fatalf("the reverse tag %q and the rule for it differ", tag)
	}
```

(add `"encoding/json"` to the test's imports).

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run ./internal/native/vnet/
git add internal/native/vnet/client.go internal/native/vnet/client_test.go
git commit -m "feat(native): a network member's client config

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The network lab — members on different nodes

**Files:**
- Modify: `internal/native/netlab/lab_test.go` (hosts `alice` 10.0.9.2 and `bob` 10.0.9.3)
- Create: `internal/native/netlab/vnet_test.go`
- Keep: `internal/native/netlab/reverse_test.go` (commit it with this task: it holds `runGame`, `tcpAsk`, `udpAsk`)

**Interfaces:**
- Consumes: masters (`startMasters`, `putSpec`, `startNode` from `masters_test.go`), admin `PUT /users/{id}`, `/api/v1`, `vnet.ClientConfig`, `runGame`.

- [ ] **Step 1: Write the lab test**

Add to `hosts` in `lab_test.go`:

```go
	{"alice", "10.0.9.2", nil},
	{"bob", "10.0.9.3", nil},
```

`vnet_test.go`:

```go
//go:build netlab

package netlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func (f *fleet) user(t *testing.T, id string) vnet.User {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf("fi1m")+":8080/users/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u vnet.User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil || u.IP == "" {
		t.Fatalf("user %s: %v %+v", id, err, u)
	}
	return u
}

func (f *fleet) api(t *testing.T, via, token, method, path, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+ipOf(masterHosts[via])+":8080/api/v1"+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return out
}

// member runs a member's device: the game on its own loopback, its Xray
// tunnel to entry, and a local port that reaches peerIP:25565 through it.
func (f *fleet) member(t *testing.T, host string, u vnet.User, entry, peerIP string) interface{ Kill() error } {
	t.Helper()
	spawn(t, host, "game", "NETLAB_ADDR=127.0.0.1:25565")
	cfg, err := vnet.ClientConfig(u, vnet.ClientEntry{
		Address: entry, Port: 443, SNI: entry, Host: entry, Network: "ws", Path: "/ws-vpn",
	}, []map[string]any{{
		"tag": "to-peer", "listen": ipOf(host), "port": 30000, "protocol": "tunnel",
		"settings": map[string]any{"address": peerIP, "port": 25565, "network": "tcp,udp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.dir, host+".json")
	os.WriteFile(file, cfg, 0o600)
	return spawnCmd(t, host, nil, f.xray, "run", "-c", file).Process
}

func vnetNode(name, host string) nodespec.Spec {
	return nodespec.Spec{
		Node: name,
		Inbounds: []nodespec.Inbound{
			{Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: host, Route: nodespec.Direct, VNet: true},
			{Tag: "cascade", Port: 12060, Network: "ws", Path: "/ws-net", Host: host, Route: nodespec.Direct,
				Clients: []nodespec.Client{{Email: vnet.CascadeUser, ID: serversID}}},
		},
		VNetHop:     &nodespec.Hop{Address: host, Port: 443, ID: serversID, Network: "ws", Path: "/ws-net", SNI: host, Host: host},
		DirectAllow: []string{"10.0.0.0/16"},
	}
}

// Spec tests 1, 2, 4, 5: alice on ru4 and bob on de2 play over the network
// both ways, TCP and UDP; carol outside it cannot; bob moves to ru4 and is
// back; alice's node dies and she is back once she reconnects elsewhere;
// bob removed loses alice.
func TestMembersOnDifferentNodes(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4, de2 := vnetNode("ru4", "ru4.lab.firewalk.test"), vnetNode("de2", "de2.lab.firewalk.test")
	ru4.Certs = []nodespec.Cert{f.cert(t, "ru4.lab.firewalk.test")}
	de2.Certs = []nodespec.Cert{f.cert(t, "de2.lab.firewalk.test")}
	alice, bob, carol := f.user(t, "alice"), f.user(t, "bob"), f.user(t, "carol")
	f.putSpec(t, "fi1", ru4)
	f.putSpec(t, "ru2", de2)
	f.startNode(t, "ru4")
	f.startNode(t, "de2")

	f.api(t, "fi1", alice.Token, "POST", "/network", `{"title":"игра"}`)
	inv := f.api(t, "de2", alice.Token, "POST", "/network/invites", `{"ttl":"1h","uses":1}`)
	f.api(t, "ru2", bob.Token, "POST", "/network/join", fmt.Sprintf(`{"code":%q}`, inv["code"]))

	aliceDev := f.member(t, "alice", alice, "ru4.lab.firewalk.test", bob.IP)
	bobDev := f.member(t, "bob", bob, "de2.lab.firewalk.test", alice.IP)
	f.member(t, "client", carol, "ru4.lab.firewalk.test", bob.IP)

	reaches := func(host, what string) {
		t.Helper()
		eventually(t, 60*time.Second, what, func() (bool, string) {
			got, err := tcpAsk(ipOf(host)+":30000", "hi")
			if err != nil {
				return false, err.Error()
			}
			return got == "tcp from player: hi", got
		})
	}
	reaches("alice", "alice (ru4) reaches bob (de2) over TCP")
	reaches("bob", "bob (de2) reaches alice (ru4) over TCP")
	if got, err := udpAsk(ipOf("alice")+":30000", "u"); err != nil || got != "udp from player: u" {
		t.Fatalf("UDP alice → bob: %q %v", got, err)
	}
	if got, err := tcpAsk(ipOf("client")+":30000", "hi"); err == nil {
		t.Fatalf("carol, outside the network, reached bob: %q", got)
	}

	// test 2: bob moves to ru4
	_ = bobDev.Kill()
	bobDev = f.member(t, "bob", bob, "ru4.lab.firewalk.test", alice.IP)
	reaches("alice", "bob is reachable again after moving to ru4")

	// test 5: alice's node (ru4) dies; alice reconnects to de2
	_ = f.nodes["ru4"].Kill()
	_ = aliceDev.Kill()
	aliceDev = f.member(t, "alice", alice, "de2.lab.firewalk.test", bob.IP)
	_ = bobDev.Kill()
	bobDev = f.member(t, "bob", bob, "de2.lab.firewalk.test", alice.IP)
	reaches("alice", "alice is back through de2 after ru4 died")

	// test 4: bob removed loses alice
	f.api(t, "fi1", alice.Token, "DELETE", "/network/members/bob", "")
	eventually(t, 30*time.Second, "bob lost the network", func() (bool, string) {
		got, err := tcpAsk(ipOf("bob")+":30000", "hi")
		if err != nil {
			return true, err.Error()
		}
		return false, got
	})
	_ = aliceDev
}
```

`startNode` must record the node's process: in `masters_test.go`, add `nodes map[string]interface{ Kill() error }` to `fleet` and set `f.nodes[node] = cmd.Process` in `startNode` (initialize the map in `startMasters`).

- [ ] **Step 2: Run the lab test**

Run: `cd /home/claud/projects/fb-ui-native && timeout 900 tools/netlab/run.sh -test.run TestMembersOnDifferentNodes -test.timeout 12m 2>&1 | grep -E "vnet_test.go|^--- |^(PASS|FAIL)"`
Expected: PASS. If a step fails, read the per-host logs the lab prints on failure (fbnode's "network routes:" and "presence:" lines, the client logs) before changing code.

- [ ] **Step 3: Prove it bites** — in `vroute.Patch`, stop emitting the `case node != ""` (remote) rules; re-run: FAIL at "alice (ru4) reaches bob (de2)"; restore.

- [ ] **Step 4: The whole lab and unit suite**

Run: `go test ./internal/native/... && timeout 1200 tools/netlab/run.sh -test.timeout 18m 2>&1 | grep -E "^--- |^(PASS|FAIL)"`
Expected: every test PASS (the earlier six plus `TestReverseReachesThePlayer` and `TestMembersOnDifferentNodes`).

- [ ] **Step 5: Docs and commit**

In `docs/design/native-vnet.md`, "Order of work" item 1: append `— done (TestMembersOnDifferentNodes)` and one sentence on anything the lab changed in the design.

```bash
golangci-lint run ./internal/native/...
git add internal/native/netlab docs/design/native-vnet.md
git commit -m "test(native): network members on different nodes in the lab

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
