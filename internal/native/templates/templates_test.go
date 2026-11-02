package templates_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templates"
)

type mem struct{ rows map[string]map[string]any }

func newMem() *mem { return &mem{rows: map[string]map[string]any{}} }

func (m *mem) Set(_ context.Context, _, key string, fields map[string]any) (oplog.Op, error) {
	m.rows[key] = fields
	return oplog.Op{}, nil
}

func (m *mem) Delete(_ context.Context, _, key string) (oplog.Op, error) {
	delete(m.rows, key)
	return oplog.Op{}, nil
}

func (m *mem) Get(_ string, key string) (map[string]json.RawMessage, bool) {
	f, ok := m.rows[key]
	if !ok {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	for k, v := range f {
		out[k], _ = json.Marshal(v)
	}
	return out, true
}

func (m *mem) Keys(string) []string {
	var out []string
	for k := range m.rows {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

const uid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

func entrySpec() nodespec.Spec {
	return nodespec.Spec{
		Node: "ignored",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: templates.NodeDomain, Route: nodespec.ProxyTag("de"),
		}},
		Exits: []nodespec.Exit{{Country: "de", Hops: []nodespec.Hop{{
			Address: templates.NodeDomain, Port: 443, ID: uid, Network: "ws", Path: "/ws-net",
			SNI: templates.NodeDomain, Host: templates.NodeDomain,
		}}}},
		DirectAllow: []string{"10.0.0.0/16"},
	}
}

func TestSaveListGetDelete(t *testing.T) {
	ctx := context.Background()
	st := newMem()
	tpl, err := templates.SaveTemplate(ctx, st, st, templates.Save{Name: "entry-de", Remark: "RU→DE", Spec: entrySpec()})
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Name != "entry-de" || tpl.Spec.Node != "ignored" {
		t.Fatalf("%+v", tpl)
	}
	if _, err := templates.SaveTemplate(ctx, st, st, templates.Save{Name: "entry-de", Spec: entrySpec()}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := templates.SaveTemplate(ctx, st, st, templates.Save{Name: "entry-de", Spec: entrySpec(), Overwrite: true}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, ok := templates.Get(st, "entry-de")
	if !ok || got.Spec.Node != "" || got.Remark != "" {
		t.Fatalf("get %+v %v", got, ok)
	}
	if names := templates.List(st); len(names) != 1 || names[0].Name != "entry-de" {
		t.Fatalf("list %+v", names)
	}
	if err := templates.Delete(ctx, st, "entry-de"); err != nil {
		t.Fatal(err)
	}
	if _, ok := templates.Get(st, "entry-de"); ok {
		t.Fatal("not deleted")
	}
}

func TestValidateRefusesClientsAndEmpty(t *testing.T) {
	if err := templates.Validate(templates.Template{Name: "x"}); err == nil {
		t.Fatal("an empty template was accepted")
	}
	s := entrySpec()
	s.Inbounds[0].Clients = []nodespec.Client{{Email: "alice", ID: uid}}
	if err := templates.Validate(templates.Template{Name: "x", Spec: s}); err == nil ||
		!strings.Contains(err.Error(), "carries clients") {
		t.Fatalf("clients: %v", err)
	}
	for _, bad := range []string{"", "-x", "a/b", strings.Repeat("a", 64)} {
		if err := templates.Validate(templates.Template{Name: bad, Spec: entrySpec()}); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestFillReplacesPlaceholders(t *testing.T) {
	got := templates.Fill(entrySpec(), "ru7", "ru7.walk.example")
	if got.Node != "ru7" || got.Inbounds[0].Host != "ru7.walk.example" {
		t.Fatalf("%+v", got)
	}
	hop := got.Exits[0].Hops[0]
	if hop.Address != "ru7.walk.example" || hop.SNI != "ru7.walk.example" || hop.Host != "ru7.walk.example" {
		t.Fatalf("hop %+v", hop)
	}
	if left := templates.Unfilled(got); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if _, err := nodespec.Render(got); err != nil {
		t.Fatalf("filled spec does not render: %v", err)
	}
}

func TestFillReportsMissingDomain(t *testing.T) {
	got := templates.Fill(entrySpec(), "ru7", "")
	if left := templates.Unfilled(got); !slices.Equal(left, []string{templates.NodeDomain}) {
		t.Fatalf("left %v", left)
	}
}

func TestFillUsesANameInTagsAndRules(t *testing.T) {
	s := nodespec.Spec{
		Inbounds: []nodespec.Inbound{{Tag: "vpn-{{NODE_NAME}}", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct}},
		Rules:    []nodespec.Rule{{Name: "rule-{{NODE_NAME}}", Domains: []string{"{{NODE_NAME}}.example"}, Route: nodespec.Direct}},
		Certs:    []nodespec.Cert{{CertPEM: "c", KeyPEM: "k"}},
		CertIDs:  []string{"node:{{NODE_NAME}}"},
	}
	got := templates.Fill(s, "de2", "de2.example")
	if got.Inbounds[0].Tag != "vpn-de2" || got.Rules[0].Name != "rule-de2" ||
		got.Rules[0].Domains[0] != "de2.example" || got.CertIDs[0] != "node:de2" {
		t.Fatalf("%+v", got)
	}
}

// Filling a template for one node must not rewrite it for the next.
func TestFillDoesNotMutateTheTemplate(t *testing.T) {
	tpl := entrySpec()
	_ = templates.Fill(tpl, "ru7", "ru7.walk.example")
	if tpl.Inbounds[0].Host != templates.NodeDomain || tpl.Exits[0].Hops[0].Address != templates.NodeDomain {
		t.Fatalf("the template was rewritten: %+v", tpl)
	}
}
