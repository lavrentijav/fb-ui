package subsmod_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subparam"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subsmod"
)

func TestDeletingSubscriptionRemovesCustomParams(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	auth, err := apiauth.Create(ctx, h, "root", module.ScopeAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Set(ctx, subauth.Table, "tok", map[string]any{"email": "alice", "enable": true}); err != nil {
		t.Fatal(err)
	}
	if err := subparam.Set(ctx, h, "tok", subparam.Params{"tg_id": "42"}); err != nil {
		t.Fatal(err)
	}

	api := apiserver.New(h, []module.Module{subsmod.New()})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/subs/tok", nil)
	req.Header.Set("Authorization", "Bearer "+auth)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete subscription: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := h.Get(subparam.Table, "tok"); ok {
		t.Fatal("custom params survived the subscription delete")
	}
	if _, ok := h.Get(subauth.Table, "tok"); ok {
		t.Fatal("subscription survived its delete")
	}
}
