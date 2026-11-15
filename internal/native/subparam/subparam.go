// Package subparam keeps operator-defined key/value parameters attached to a
// subscription token. They are free-form (a flag, a plan name, a routing hint)
// and are not interpreted by the master; the API and the subscription page
// expose them for the operator and the sales bot.
package subparam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds one params object per subscription token.
const Table = "subparam"

// MaxParams bounds one subscription's custom map.
const MaxParams = 64

var (
	ErrToken   = errors.New("a subscription token is required")
	ErrKey     = errors.New("a parameter key must match [A-Za-z0-9._-]{1,64}")
	ErrValue   = errors.New("a parameter value must be at most 4096 bytes")
	ErrTooMany = fmt.Errorf("at most %d parameters per subscription", MaxParams)
	keyRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Params is one subscription's custom key/value set.
type Params map[string]string

// Validate checks the key shape and the size limits.
func (p Params) Validate() error {
	if len(p) > MaxParams {
		return ErrTooMany
	}
	for key, value := range p {
		if !keyRe.MatchString(key) {
			return ErrKey
		}
		if len(value) > 4096 {
			return ErrValue
		}
	}
	return nil
}

// Get reads a subscription's parameters; a missing row is an empty set.
func Get(r module.Reader, token string) (Params, bool) {
	if strings.TrimSpace(token) == "" {
		return nil, false
	}
	rec, ok := r.Get(Table, token)
	if !ok {
		return nil, false
	}
	out := Params{}
	_ = json.Unmarshal(rec["params"], &out)
	if out == nil {
		out = Params{}
	}
	return out, true
}

// Set replaces a subscription's parameters.
func Set(ctx context.Context, w module.Writer, token string, p Params) error {
	if strings.TrimSpace(token) == "" {
		return ErrToken
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p == nil {
		p = Params{}
	}
	_, err := w.Set(ctx, Table, token, map[string]any{"params": p})
	return err
}

// Merge adds or replaces the given keys, keeping the rest.
func Merge(ctx context.Context, h module.Host, token string, p Params) (Params, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrToken
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out, _ := Get(h, token)
	if out == nil {
		out = Params{}
	}
	for key, value := range p {
		out[key] = value
	}
	if err := Set(ctx, h, token, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteKey removes one parameter, leaving an empty row when it was the last.
func DeleteKey(ctx context.Context, h module.Host, token, key string) (Params, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrToken
	}
	out, _ := Get(h, token)
	if out == nil {
		out = Params{}
	}
	delete(out, key)
	if err := Set(ctx, h, token, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the whole row.
func Delete(ctx context.Context, h module.Host, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrToken
	}
	_, err := h.Delete(ctx, Table, token)
	return err
}

// List returns every subscription's parameters, keyed by token.
func List(r module.Reader) map[string]Params {
	out := map[string]Params{}
	for _, token := range r.Keys(Table) {
		if p, ok := Get(r, token); ok {
			out[token] = p
		}
	}
	return out
}
