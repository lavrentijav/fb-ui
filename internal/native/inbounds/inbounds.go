// Package inbounds edits a node's spec: the inbounds it serves, their clients'
// shape and the reverse routes the edge opens. It is the panel's main write
// path, so it only touches the spec and lets nodes pull it.
package inbounds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// SpecTable is where a node's spec lives.
const SpecTable = "nodespec"

var (
	ErrNoNode   = errors.New("a node is required")
	ErrNoTag    = errors.New("an inbound tag is required")
	ErrExists   = errors.New("an inbound with this tag already exists on that node")
	ErrNotFound = errors.New("no such inbound on that node")
	validTag    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// Input is the API's inbound shape; a client list is deliberately absent,
// because clients come from the `client` table, not from an inbound edit.
type Input struct {
	Tag     string `json:"tag"`
	Port    int    `json:"port"`
	Network string `json:"network"`
	Path    string `json:"path"`
	Host    string `json:"host,omitempty"`
	Route   string `json:"route"`
	VNet    bool   `json:"vnet,omitempty"`
}

// Inbound builds a nodespec inbound from the input; the client list stays as
// the caller had it (Attach adds the records later).
func (in Input) Inbound(existing nodespec.Inbound) nodespec.Inbound {
	ib := existing
	ib.Tag, ib.Port, ib.Network, ib.Path, ib.Host, ib.Route, ib.VNet =
		in.Tag, in.Port, in.Network, in.Path, in.Host, in.Route, in.VNet
	if ib.Clients == nil {
		ib.Clients = []nodespec.Client{}
	}
	return ib
}

// Validate rejects an input that could not render.
func (in Input) Validate() error {
	if !validTag.MatchString(in.Tag) {
		return fmt.Errorf("bad inbound tag %q (a-z, 0-9, - _)", in.Tag)
	}
	if in.Port <= 0 || in.Port > 65535 {
		return fmt.Errorf("the port must be 1–65535")
	}
	if in.Network != "ws" && in.Network != "xhttp" {
		return fmt.Errorf("the network must be ws or xhttp")
	}
	if !strings.HasPrefix(in.Path, "/") {
		return fmt.Errorf("the path must start with /")
	}
	if in.Route == "" {
		return fmt.Errorf("a route is required (direct, block or proxy_<country>)")
	}
	return nil
}

// Spec reads a node's spec.
func Spec(r module.Reader, node string) (nodespec.Spec, bool) {
	rec, ok := r.Get(SpecTable, node)
	if !ok {
		return nodespec.Spec{}, false
	}
	var s nodespec.Spec
	if json.Unmarshal(rec["spec"], &s) != nil {
		return nodespec.Spec{}, false
	}
	return s, true
}

// Write stores a spec after rendering it, so a node never receives one it
// cannot run.
func Write(ctx context.Context, w module.Writer, node string, s nodespec.Spec) error {
	if _, err := nodespec.Render(s); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = w.Set(ctx, SpecTable, node, map[string]any{"spec": json.RawMessage(raw)})
	return err
}

// Add appends an inbound to the node's spec.
func Add(ctx context.Context, w module.Writer, r module.Reader, node string, in Input) (nodespec.Inbound, error) {
	if node == "" {
		return nodespec.Inbound{}, ErrNoNode
	}
	if err := in.Validate(); err != nil {
		return nodespec.Inbound{}, err
	}
	s, ok := Spec(r, node)
	if !ok {
		return nodespec.Inbound{}, fmt.Errorf("no spec for %s (register it first)", node)
	}
	if slices.ContainsFunc(s.Inbounds, func(ib nodespec.Inbound) bool { return ib.Tag == in.Tag }) {
		return nodespec.Inbound{}, ErrExists
	}
	ib := in.Inbound(nodespec.Inbound{})
	s.Inbounds = append(s.Inbounds, ib)
	if err := Write(ctx, w, node, s); err != nil {
		return nodespec.Inbound{}, err
	}
	return ib, nil
}

// Update replaces an inbound's shape, keeping its clients.
func Update(ctx context.Context, w module.Writer, r module.Reader, node, tag string, in Input) (nodespec.Inbound, error) {
	if node == "" {
		return nodespec.Inbound{}, ErrNoNode
	}
	if err := in.Validate(); err != nil {
		return nodespec.Inbound{}, err
	}
	s, ok := Spec(r, node)
	if !ok {
		return nodespec.Inbound{}, fmt.Errorf("no spec for %s", node)
	}
	i := slices.IndexFunc(s.Inbounds, func(ib nodespec.Inbound) bool { return ib.Tag == tag })
	if i < 0 {
		return nodespec.Inbound{}, ErrNotFound
	}
	if in.Tag != tag && slices.ContainsFunc(s.Inbounds, func(ib nodespec.Inbound) bool { return ib.Tag == in.Tag }) {
		return nodespec.Inbound{}, ErrExists
	}
	s.Inbounds[i] = in.Inbound(s.Inbounds[i])
	if err := Write(ctx, w, node, s); err != nil {
		return nodespec.Inbound{}, err
	}
	return s.Inbounds[i], nil
}

// Remove drops an inbound. It refuses while rules or exits still name it, so
// a delete cannot leave the node with a broken route.
func Remove(ctx context.Context, w module.Writer, r module.Reader, node, tag string) error {
	if node == "" {
		return ErrNoNode
	}
	s, ok := Spec(r, node)
	if !ok {
		return fmt.Errorf("no spec for %s", node)
	}
	i := slices.IndexFunc(s.Inbounds, func(ib nodespec.Inbound) bool { return ib.Tag == tag })
	if i < 0 {
		return ErrNotFound
	}
	for _, rule := range s.Rules {
		if slices.Contains(rule.Inbounds, tag) {
			return fmt.Errorf("rule %q still names inbound %q", rule.Name, tag)
		}
	}
	s.Inbounds = slices.Delete(s.Inbounds, i, i+1)
	return Write(ctx, w, node, s)
}
