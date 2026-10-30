// Package certsmod exposes the fleet's certificates over the API: their
// status (never the key) and a rollback to the pair a renewal replaced.
package certsmod

import (
	"context"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "certs" }
func (*Module) Tables() []string { return []string{certs.Table, certs.LeaseTable} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/certs/list", New: "/api/v1/certs"},
		{Method: "POST", Old: "/panel/api/certs/rollback/{id}", New: "/api/v1/certs/{id}/rollback"},
	}
}

// Run does nothing: issuing and renewing belong to the master itself, which
// owns the ACME account and the lease.
func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (*Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, certs.List(h))
	})
	r.Handle("POST", "/{id}/rollback", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if err := certs.Rollback(req.Context(), h, h, req.PathValue("id"), h.Now()); err != nil {
			apiserver.Error(w, http.StatusConflict, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "rolled back"})
	})
}
