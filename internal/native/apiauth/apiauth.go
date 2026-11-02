// Package apiauth keeps API tokens in the op log as hashes: any master can
// check a token, and no master can read one back.
package apiauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table is the op log table; its keys are the SHA-256 (hex) of the tokens.
const Table = "apitoken"

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Hash is the key a token is stored under; a session keeps it, never the
// token itself.
func Hash(token string) string { return hash(token) }

// Create makes a token and returns it; this is the only time it is visible.
// ttl 0 means it never expires.
func Create(ctx context.Context, h module.Host, name string, scope module.Scope, ttl time.Duration) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	var expires int64
	if ttl > 0 {
		expires = h.Now().Add(ttl).UnixMilli()
	}
	_, err := h.Set(ctx, Table, hash(token), map[string]any{
		"name": name, "scope": scope, "expires": expires, "at": h.Now().UnixMilli(),
	})
	return token, err
}

// Verify finds a live token and says what it may do and who it is.
func Verify(h module.Host, bearer string) (module.Scope, string, bool) {
	if bearer == "" {
		return "", "", false
	}
	rec, ok := h.Get(Table, hash(bearer))
	if !ok {
		return "", "", false
	}
	var scope module.Scope
	var name string
	var expires int64
	_ = json.Unmarshal(rec["scope"], &scope)
	_ = json.Unmarshal(rec["name"], &name)
	_ = json.Unmarshal(rec["expires"], &expires)
	if expires > 0 && h.Now().UnixMilli() >= expires {
		return "", "", false
	}
	return scope, name, true
}

// ByHash answers a stored key (a session keeps the hash, not the token).
func ByHash(h module.Host, hash string) (module.Scope, string, bool) {
	if hash == "" {
		return "", "", false
	}
	rec, ok := h.Get(Table, hash)
	if !ok {
		return "", "", false
	}
	var scope module.Scope
	var name string
	var expires int64
	_ = json.Unmarshal(rec["scope"], &scope)
	_ = json.Unmarshal(rec["name"], &name)
	_ = json.Unmarshal(rec["expires"], &expires)
	if expires > 0 && h.Now().UnixMilli() >= expires {
		return "", "", false
	}
	return scope, name, true
}

// Allows says whether a token of scope have may do what needs need: admin may
// do everything, monitor only monitoring.
func Allows(have, need module.Scope) bool {
	switch have {
	case module.ScopeAdmin:
		return true
	case module.ScopeMonitor:
		return need == module.ScopeMonitor
	}
	return false
}
