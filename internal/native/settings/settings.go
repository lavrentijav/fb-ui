// Package settings keeps the master's typed key/value settings in the op log:
// a schema names each key, its type and default, so a bad value is refused on
// write instead of at render time.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds one record per setting, keyed by name.
const Table = "setting"

// Kind is how a value is parsed and rendered.
type Kind string

const (
	KindString Kind = "string"
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindList   Kind = "list" // comma separated
	KindSecret Kind = "secret"
)

// Spec describes one setting.
type Spec struct {
	Kind    Kind
	Default string
	Section string
	// Min, when set, is the smallest accepted int.
	Min int
	// Check, when set, validates a string value (after parsing).
	Check func(string) error
}

var (
	ErrUnknownKey = errors.New("unknown setting")
	ErrBadValue   = errors.New("bad setting value")
)

// Schema is the whole set of known settings.
type Schema map[string]Spec

// Validate checks one value against its spec and returns the stored form.
func (s Schema) Validate(name, value string) (string, error) {
	spec, ok := s[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownKey, name)
	}
	switch spec.Kind {
	case KindBool:
		switch value {
		case "true", "false":
			return value, nil
		}
		return "", fmt.Errorf("%w: %s must be true or false", ErrBadValue, name)
	case KindInt:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("%w: %s must be a number", ErrBadValue, name)
		}
		if n < spec.Min {
			return "", fmt.Errorf("%w: %s must be at least %d", ErrBadValue, name, spec.Min)
		}
		return strconv.Itoa(n), nil
	case KindList:
		parts := Split(value)
		return strings.Join(parts, ","), nil
	}
	if spec.Check != nil {
		if err := spec.Check(value); err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrBadValue, name, err)
		}
	}
	return value, nil
}

// Split parses a comma-separated list, dropping blanks.
func Split(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// View is one setting as the API shows it: a secret is never sent back.
type View struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Kind      Kind   `json:"kind"`
	Section   string `json:"section,omitempty"`
	Secret    bool   `json:"secret,omitempty"`
	HasSecret bool   `json:"hasSecret,omitempty"`
}

// Get answers one value, falling back to the schema default.
func Get(r module.Reader, s Schema, name string) string {
	if rec, ok := r.Get(Table, name); ok {
		var v string
		if json.Unmarshal(rec["value"], &v) == nil {
			return v
		}
	}
	return s[name].Default
}

// Bool and Int are typed readers with the schema default.
func Bool(r module.Reader, s Schema, name string) bool { return Get(r, s, name) == "true" }
func Int(r module.Reader, s Schema, name string) int {
	n, _ := strconv.Atoi(Get(r, s, name))
	return n
}
func List(r module.Reader, s Schema, name string) []string { return Split(Get(r, s, name)) }

// All answers every known setting, masked where needed, ordered by section
// then name.
func All(r module.Reader, s Schema) []View {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sortNames(names)
	var out []View
	for _, name := range names {
		spec := s[name]
		v := Get(r, s, name)
		view := View{Name: name, Kind: spec.Kind, Section: spec.Section}
		if spec.Kind == KindSecret {
			view.Secret = true
			view.HasSecret = v != ""
		} else {
			view.Value = v
		}
		out = append(out, view)
	}
	return out
}

// Set validates and writes one setting. An empty secret keeps the stored one,
// so a masked form can be saved without resending the secret.
func Set(ctx context.Context, h module.Host, s Schema, name, value string) error {
	spec, ok := s[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, name)
	}
	if spec.Kind == KindSecret && value == "" {
		if _, exists := h.Get(Table, name); exists {
			return nil
		}
	}
	v, err := s.Validate(name, value)
	if err != nil {
		return err
	}
	_, err = h.Set(ctx, Table, name, map[string]any{"value": v})
	return err
}

// SetMany applies a map, stopping at the first refusal.
func SetMany(ctx context.Context, h module.Host, s Schema, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sortNames(names)
	for _, name := range names {
		if err := Set(ctx, h, s, name, values[name]); err != nil {
			return err
		}
	}
	return nil
}

func sortNames(names []string) { sort.Strings(names) }

// CheckURL accepts only an http(s) URL, for webhook-style settings.
func CheckURL(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http(s) URL")
	}
	return nil
}

var botToken = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,}$`)

// CheckTelegramToken accepts the BotFather format.
func CheckTelegramToken(v string) error {
	if v == "" {
		return nil
	}
	if !botToken.MatchString(v) {
		return errors.New("looks unlike a bot token (123456:ABC…)")
	}
	return nil
}
