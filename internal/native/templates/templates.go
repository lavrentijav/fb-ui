// Package templates keeps named node specs and stamps them onto nodes: the
// same spec laid down on many servers, with the target's name and public
// domain filled in.
package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Table holds the templates in the op log, keyed by name.
const Table = "template"

// Placeholders a template may carry; Apply replaces them.
const (
	NodeName   = "{{NODE_NAME}}"
	NodeDomain = "{{NODE_DOMAIN}}"
)

var (
	ErrBadName    = errors.New("a template name is required (1–63 chars: letters, digits, - _ .)")
	ErrNoItems    = errors.New("the template has no inbounds and no exits")
	ErrExists     = errors.New("a template with this name already exists")
	ErrNoTemplate = errors.New("no such template")
	ErrNoNodes    = errors.New("pick at least one node")
	validName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// Template is one saved spec under a name.
type Template struct {
	Name   string        `json:"name"`
	Remark string        `json:"remark,omitempty"`
	Spec   nodespec.Spec `json:"spec"`
}

// Save is what a caller stores.
type Save struct {
	Name      string        `json:"name"`
	Remark    string        `json:"remark,omitempty"`
	Spec      nodespec.Spec `json:"spec"`
	Overwrite bool          `json:"overwrite"`
}

// Writer writes template records; module.Host satisfies it.
type Writer interface {
	Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error)
	Delete(ctx context.Context, table, key string) (oplog.Op, error)
}

// Reader reads the projected state.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

// Validate checks a template can be laid onto a node: a spec without clients
// (clients arrive from the network or the register step) and with a name.
func Validate(t Template) error {
	if !validName.MatchString(t.Name) {
		return ErrBadName
	}
	if len(t.Spec.Inbounds) == 0 && len(t.Spec.Exits) == 0 {
		return ErrNoItems
	}
	for _, ib := range t.Spec.Inbounds {
		if len(ib.Clients) > 0 {
			return fmt.Errorf("template %q: inbound %q carries clients; a template describes the shape, not people", t.Name, ib.Tag)
		}
	}
	return nil
}

// Save records a template, refusing a duplicate name unless Overwrite.
func SaveTemplate(ctx context.Context, w Writer, r Reader, s Save) (Template, error) {
	t := Template{Name: strings.TrimSpace(s.Name), Remark: strings.TrimSpace(s.Remark), Spec: s.Spec}
	if err := Validate(t); err != nil {
		return Template{}, err
	}
	if _, exists := r.Get(Table, t.Name); exists && !s.Overwrite {
		return Template{}, ErrExists
	}
	raw, err := json.Marshal(t.Spec)
	if err != nil {
		return Template{}, err
	}
	if _, err := w.Set(ctx, Table, t.Name, map[string]any{"remark": t.Remark, "spec": json.RawMessage(raw)}); err != nil {
		return Template{}, err
	}
	return t, nil
}

// Get reads one template.
func Get(r Reader, name string) (Template, bool) {
	rec, ok := r.Get(Table, name)
	if !ok {
		return Template{}, false
	}
	t := Template{Name: name}
	_ = json.Unmarshal(rec["remark"], &t.Remark)
	_ = json.Unmarshal(rec["spec"], &t.Spec)
	t.Spec.Node = "" // the template does not name a node
	return t, true
}

// List returns every template sorted by name.
func List(r Reader) []Template {
	var out []Template
	for _, name := range r.Keys(Table) {
		if t, ok := Get(r, name); ok {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Delete removes a template.
func Delete(ctx context.Context, w Writer, name string) error {
	_, err := w.Delete(ctx, Table, name)
	return err
}

// Fill replaces the placeholders with the target's name and public domain and
// names the spec after the node. It works on a deep copy: the caller's spec
// (often a template) shares its slices and must not be rewritten in place.
func Fill(spec nodespec.Spec, node, domain string) nodespec.Spec {
	spec = clone(spec)
	sub := func(s string) string {
		s = strings.ReplaceAll(s, NodeName, node)
		if domain != "" {
			s = strings.ReplaceAll(s, NodeDomain, domain)
		}
		return s
	}
	spec.Node = node
	for i := range spec.Inbounds {
		ib := &spec.Inbounds[i]
		ib.Tag, ib.Host, ib.Path = sub(ib.Tag), sub(ib.Host), sub(ib.Path)
	}
	for i := range spec.Exits {
		e := &spec.Exits[i]
		for j := range e.Hops {
			h := &e.Hops[j]
			h.Address, h.SNI, h.Host, h.Path = sub(h.Address), sub(h.SNI), sub(h.Host), sub(h.Path)
		}
	}
	for i := range spec.Rules {
		r := &spec.Rules[i]
		r.Name = sub(r.Name)
		r.Domains = mapStrings(r.Domains, sub)
		r.IPs = mapStrings(r.IPs, sub)
	}
	for i := range spec.Reverse {
		r := &spec.Reverse[i]
		r.SNI, r.Target, r.Path = sub(r.SNI), sub(r.Target), sub(r.Path)
	}
	spec.CertIDs = mapStrings(spec.CertIDs, sub)
	if spec.VNetHop != nil {
		h := *spec.VNetHop
		h.Address, h.SNI, h.Host, h.Path = sub(h.Address), sub(h.SNI), sub(h.Host), sub(h.Path)
		spec.VNetHop = &h
	}
	return spec
}

func clone(spec nodespec.Spec) nodespec.Spec {
	raw, err := json.Marshal(spec)
	if err != nil {
		return spec
	}
	var out nodespec.Spec
	if json.Unmarshal(raw, &out) != nil {
		return spec
	}
	return out
}

func mapStrings(in []string, f func(string) string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

// Unfilled lists the placeholders a spec still has after a fill, so a caller
// can say "this template wants a domain you did not give".
func Unfilled(spec nodespec.Spec) []string {
	raw, _ := json.Marshal(spec)
	seen := map[string]bool{}
	for _, p := range []string{NodeName, NodeDomain} {
		if strings.Contains(string(raw), p) {
			seen[p] = true
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// SpecTable is where a node's spec lives; the same table the masters render
// from, so an applied template is just another spec write.
const SpecTable = "nodespec"

// ApplyRequest names the nodes and what fills their placeholders.
type ApplyRequest struct {
	Nodes  []string          `json:"nodes"`
	Domain map[string]string `json:"domain"` // node -> public domain
	Names  map[string]string `json:"names"`  // node -> {{NODE_NAME}} (default: the node name)
	DryRun bool              `json:"dryRun"`
}

// ApplyResult is what happened on one node.
type ApplyResult struct {
	Node    string   `json:"node"`
	Applied bool     `json:"applied"`
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Stamp fills the template for each node and, unless DryRun, writes the spec.
// A node whose placeholders cannot be filled is reported, not written.
func Stamp(ctx context.Context, r Reader, w Writer, name string, req ApplyRequest) ([]ApplyResult, error) {
	tpl, ok := Get(r, name)
	if !ok {
		return nil, ErrNoTemplate
	}
	if len(req.Nodes) == 0 {
		return nil, ErrNoNodes
	}
	var out []ApplyResult
	for _, node := range req.Nodes {
		nm := req.Names[node]
		if nm == "" {
			nm = node
		}
		spec := Fill(tpl.Spec, nm, req.Domain[node])
		spec.Node = node
		res := ApplyResult{Node: node}
		if missing := Unfilled(spec); len(missing) > 0 {
			res.Missing = missing
			out = append(out, res)
			continue
		}
		if _, err := nodespec.Render(spec); err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		if !req.DryRun {
			raw, err := json.Marshal(spec)
			if err != nil {
				res.Error = err.Error()
				out = append(out, res)
				continue
			}
			if _, err := w.Set(ctx, SpecTable, node, map[string]any{"spec": json.RawMessage(raw)}); err != nil {
				res.Error = err.Error()
				out = append(out, res)
				continue
			}
		}
		res.Applied = true
		out = append(out, res)
	}
	return out, nil
}
