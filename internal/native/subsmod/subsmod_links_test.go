package subsmod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subsmod"
)

// attachedClients stands in for clientsmod: the client lives outside the
// stored spec and is attached only when a master renders it for a node.
type attachedClients struct{}

func (attachedClients) Name() string                           { return "test-attached-clients" }
func (attachedClients) Tables() []string                       { return nil }
func (attachedClients) Mount(module.Router, module.Host)       {}
func (attachedClients) Legacy() []module.LegacyRoute           { return nil }
func (attachedClients) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (attachedClients) ContributeSpec(_ module.Reader, _ string, s *nodespec.Spec) {
	for i := range s.Inbounds {
		s.Inbounds[i].Clients = append(s.Inbounds[i].Clients, nodespec.Client{
			Email: "alice", ID: "11111111-1111-4111-8111-111111111111",
		})
	}
}

// The operator's list must count the links /sub will serve, including the
// clients a module attaches at render time.
func TestSubscriptionListCountsRenderedLinks(t *testing.T) {
	module.Reset()
	module.Register(attachedClients{})
	t.Cleanup(module.Reset)

	h := modtest.NewHost("m1")
	auth, err := apiauth.Create(context.Background(), h, "root", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(nodespec.Spec{Node: "ru", Inbounds: []nodespec.Inbound{{
		Tag: "in", Port: 443, Network: "ws", Path: "/ws", Host: "ru.firewalk.top",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Set(context.Background(), subsmod.SpecTable, "ru", map[string]any{"spec": json.RawMessage(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Set(context.Background(), subauth.Table, "tok", map[string]any{"email": "alice", "enable": true}); err != nil {
		t.Fatal(err)
	}

	api := apiserver.New(h, []module.Module{subsmod.New()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subs", nil)
	req.Header.Set("Authorization", "Bearer "+auth)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("subs: %d %s", rec.Code, rec.Body)
	}
	var rows []struct {
		Links int `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("subs: %v %s", err, rec.Body)
	}
	if len(rows) != 1 || rows[0].Links != 1 {
		t.Fatalf("links = %+v, want one rendered link", rows)
	}
}
