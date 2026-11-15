// Package subauth is the subscription's own account: a login name and a
// generated password let the subscriber open the management page and reach
// only their own subscription, never the panel.
package subauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds the subscriptions, keyed by their public token.
const Table = "subtoken"

// SessionTable holds live management sessions, keyed by sha256 of the cookie.
const SessionTable = "subsession"

// CookieName is the management session cookie.
const CookieName = "fbui_sub"

// SessionTTL bounds a management session.
const SessionTTL = 30 * 24 * time.Hour

var (
	ErrNoSubscription = errors.New("no such subscription")
	ErrNoUser         = errors.New("no such login")
	ErrBadPassword    = errors.New("wrong password")
	ErrUser           = errors.New("a login name is required (3–32 chars: letters, digits, . _ -)")
	ErrDisabled       = errors.New("this subscription is disabled")
	ErrAlias          = errors.New("an alias must be 1–128 chars: letters, digits, . _ -")
	ErrAliasTaken     = errors.New("that alias already belongs to another subscription")
	validUser         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,31}$`)
	validToken        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Record is one subscription as the management side sees it. Alias records
// carry the primary token in AliasOf and have no login of their own.
type Record struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	User     string `json:"user,omitempty"`
	PassHash string `json:"passHash,omitempty"`
	Enable   bool   `json:"enable"`
	AliasOf  string `json:"aliasOf,omitempty"`
}

func (r Record) Active() bool { return r.Enable }

// ValidUser reports whether name can be a login.
func ValidUser(name string) bool { return validUser.MatchString(name) }

// ValidToken reports whether token can be a public subscription token.
func ValidToken(token string) bool { return validToken.MatchString(token) }

// EnsureAlias makes alias resolve to primary's email. The alias has no login
// of its own; the primary record still owns the password and the management page.
func EnsureAlias(ctx context.Context, h module.Host, alias, primary string, enable bool) error {
	if !ValidToken(alias) {
		return ErrAlias
	}
	p, ok := Get(h, primary)
	if !ok || p.AliasOf != "" {
		return ErrNoSubscription
	}
	if existing, ok := Get(h, alias); ok {
		if existing.AliasOf != primary || existing.Email != p.Email {
			return ErrAliasTaken
		}
		enable = enable || existing.Enable
	}
	_, err := h.Set(ctx, Table, alias, map[string]any{
		"email": p.Email, "enable": enable && p.Enable, "aliasOf": primary,
	})
	return err
}

// Aliases lists the alias tokens of a primary subscription.
func Aliases(r Reader, primary string) []string {
	out := make([]string, 0)
	for _, token := range r.Keys(Table) {
		rec, ok := Get(r, token)
		if ok && rec.AliasOf == primary {
			out = append(out, token)
		}
	}
	slices.Sort(out)
	return out
}

// DeleteAlias removes alias, but only when it belongs to primary.
func DeleteAlias(ctx context.Context, h module.Host, alias, primary string) error {
	rec, ok := Get(h, alias)
	if !ok || rec.AliasOf != primary {
		return ErrNoSubscription
	}
	_, err := h.Delete(ctx, Table, alias)
	return err
}

// Hash is the session key: a cookie's value never appears in the op log.
func Hash(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

// NewPassword makes a readable 16-character password.
func NewPassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}

// Password stores a bcrypt hash, so the op log never carries the password.
func Password(pass string) (string, error) {
	if len(pass) < 8 {
		return "", errors.New("the password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(h), err
}

// Check compares a password with a stored hash.
func Check(hash, pass string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil
}

// Reader reads the projected state.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

// Get answers the subscription behind a public token.
func Get(r Reader, token string) (Record, bool) {
	rec, ok := r.Get(Table, token)
	if !ok {
		return Record{}, false
	}
	out := Record{Token: token, Enable: true}
	_ = json.Unmarshal(rec["email"], &out.Email)
	_ = json.Unmarshal(rec["user"], &out.User)
	_ = json.Unmarshal(rec["pass"], &out.PassHash)
	_ = json.Unmarshal(rec["aliasOf"], &out.AliasOf)
	if raw, ok := rec["enable"]; ok {
		_ = json.Unmarshal(raw, &out.Enable)
	}
	return out, true
}

// ByUser finds the subscription with this login.
func ByUser(r Reader, user string) (Record, bool) {
	user = strings.ToLower(user)
	for _, token := range r.Keys(Table) {
		rec, ok := r.Get(Table, token)
		if !ok {
			continue
		}
		var name string
		_ = json.Unmarshal(rec["user"], &name)
		if strings.ToLower(name) == user {
			return Get(r, token)
		}
	}
	return Record{}, false
}

// Login checks a login name and password and answers the subscription.
func Login(r Reader, user, pass string) (Record, error) {
	rec, ok := ByUser(r, user)
	if !ok || rec.PassHash == "" {
		return Record{}, ErrNoUser
	}
	if !rec.Active() {
		return Record{}, ErrDisabled
	}
	if !Check(rec.PassHash, pass) {
		return Record{}, ErrBadPassword
	}
	return rec, nil
}

// Session is one live management login.
type Session struct {
	Token   string
	Expires int64
}

// NewSession records a session for token and answers the cookie value.
func NewSession(ctx context.Context, h module.Host, token string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	cookie := hex.EncodeToString(buf)
	expires := h.Now().Add(SessionTTL)
	if _, err := h.Set(ctx, SessionTable, Hash(cookie), map[string]any{
		"token": token, "expires": expires.UnixMilli(),
	}); err != nil {
		return "", time.Time{}, err
	}
	return cookie, expires, nil
}

// FromCookie answers the subscription behind a cookie, if the session is live.
func FromCookie(r Reader, cookie string, now time.Time) (Record, bool) {
	if cookie == "" {
		return Record{}, false
	}
	rec, ok := r.Get(SessionTable, Hash(cookie))
	if !ok {
		return Record{}, false
	}
	var token string
	var expires int64
	_ = json.Unmarshal(rec["token"], &token)
	_ = json.Unmarshal(rec["expires"], &expires)
	sub, ok := Get(r, token)
	if !ok || !sub.Active() {
		return Record{}, false
	}
	if expires > 0 && now.UnixMilli() >= expires {
		return Record{}, false
	}
	return sub, true
}

// EndSession drops a cookie's session.
func EndSession(ctx context.Context, h module.Host, cookie string) error {
	_, err := h.Delete(ctx, SessionTable, Hash(cookie))
	return err
}
