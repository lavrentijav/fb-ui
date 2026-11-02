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

type User struct{ ID, Email, UUID, RevUUID, Token, IP string }

// RevEmail is the user's reverse-tunnel identity on the same inbound: Xray
// forbids a reverse-enabled user from making forward requests, so one user
// cannot both expose its tunnel and send into the network.
func RevEmail(email string) string { return email + "#rev" }

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

// waitFor gives a write made through another master time to arrive here.
func (s *Store) waitFor(ctx context.Context, ok func() bool) bool {
	for range 20 {
		if ok() {
			return true
		}
		_, _ = s.Master.CatchUp(ctx)
		if ok() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ok()
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

// CreateUser gives id a VLESS id, an API token and a permanent address, re-read
// after it replicates: two masters can hand out the same one at once.
func (s *Store) CreateUser(ctx context.Context, id string) (User, error) {
	if u, ok := s.User(id); ok {
		return u, nil
	}
	u := User{ID: id, Email: id, UUID: uuid4(), RevUUID: uuid4(), Token: random(24)}
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
		"email": u.Email, "uuid": u.UUID, "revuuid": u.RevUUID, "token": u.Token, "ip": u.IP,
	})
	return u, err
}

func (s *Store) User(id string) (User, bool) {
	rec, ok := s.State.Get(UserTable, id)
	if !ok {
		return User{}, false
	}
	return User{ID: id, Email: str(rec, "email"), UUID: str(rec, "uuid"), RevUUID: str(rec, "revuuid"), Token: str(rec, "token"), IP: str(rec, "ip")}, true
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
	s.waitFor(ctx, func() bool { _, ok := s.NetworkOf(by); return ok })
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
	s.waitFor(ctx, func() bool { _, ok := s.State.Get(InviteTable, code); return ok })
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
