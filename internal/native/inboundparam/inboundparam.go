// Package inboundparam keeps operator-defined key/value parameters attached to
// an inbound, keyed by "node/tag". They are free-form (a flag, a routing hint,
// a billing label); one conventional key is "reserved", which tells the sales
// bot not to add the inbound to new subscriptions.
package inboundparam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds one params object per inbound.
const Table = "inboundparam"

// MaxParams bounds one inbound's custom map.
const MaxParams = 64

var (
	ErrRef     = errors.New("a node and inbound tag are required")
	ErrKey     = errors.New("a parameter key must match [A-Za-z0-9._-]{1,64}")
	ErrValue   = errors.New("a parameter value must be at most 4096 bytes")
	ErrTooMany = fmt.Errorf("at most %d parameters per inbound", MaxParams)
	keyRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Params is one inbound's custom key/value set.
type Params map[string]string

// Key names the row for one inbound across the fleet.
func Key(node, tag string) string { return node + "/" + tag }

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

// Get reads an inbound's parameters; a missing row is an empty set.
func Get(r module.Reader, node, tag string) (Params, bool) {
	if strings.TrimSpace(node) == "" || strings.TrimSpace(tag) == "" {
		return nil, false
	}
	rec, ok := r.Get(Table, Key(node, tag))
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

// Set replaces an inbound's parameters.
func Set(ctx context.Context, w module.Writer, node, tag string, p Params) error {
	if strings.TrimSpace(node) == "" || strings.TrimSpace(tag) == "" {
		return ErrRef
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p == nil {
		p = Params{}
	}
	_, err := w.Set(ctx, Table, Key(node, tag), map[string]any{"params": p})
	return err
}

// Merge adds or replaces the given keys, keeping the rest.
func Merge(ctx context.Context, h module.Host, node, tag string, p Params) (Params, error) {
	if strings.TrimSpace(node) == "" || strings.TrimSpace(tag) == "" {
		return nil, ErrRef
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out, _ := Get(h, node, tag)
	if out == nil {
		out = Params{}
	}
	for key, value := range p {
		out[key] = value
	}
	if err := Set(ctx, h, node, tag, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteKey removes one parameter, leaving an empty row when it was the last.
func DeleteKey(ctx context.Context, h module.Host, node, tag, key string) (Params, error) {
	if strings.TrimSpace(node) == "" || strings.TrimSpace(tag) == "" {
		return nil, ErrRef
	}
	out, _ := Get(h, node, tag)
	if out == nil {
		out = Params{}
	}
	delete(out, key)
	if err := Set(ctx, h, node, tag, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the whole row.
func Delete(ctx context.Context, h module.Host, node, tag string) error {
	if strings.TrimSpace(node) == "" || strings.TrimSpace(tag) == "" {
		return ErrRef
	}
	_, err := h.Delete(ctx, Table, Key(node, tag))
	return err
}

// List returns every inbound's parameters, keyed by "node/tag".
func List(r module.Reader) map[string]Params {
	out := map[string]Params{}
	for _, key := range r.Keys(Table) {
		node, tag, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		if p, ok := Get(r, node, tag); ok {
			out[key] = p
		}
	}
	return out
}

// Reserved reports whether the operator marked this inbound as not to be added
// to new subscriptions. The value is truthy: anything but "", "false", "0",
// "no" or "off" (case-insensitive).
func Reserved(p Params) bool {
	switch strings.ToLower(strings.TrimSpace(p["reserved"])) {
	case "", "false", "0", "no", "off":
		return false
	}
	return true
}
