// Package hostsmod exposes the subscription's advertised addresses over the
// API: list, add, update and remove per node and inbound.
package hostsmod

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "hosts" }
func (*Module) Tables() []string { return []string{hosts.Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/hosts/list", New: "/api/v1/hosts"},
		{Method: "POST", Old: "/panel/api/hosts/add", New: "/api/v1/hosts"},
		{Method: "POST", Old: "/panel/api/hosts/del/{id}", New: "/api/v1/hosts/{id}", NewMethod: "DELETE"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, hosts.List(h))
	})
	r.Handle("POST", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var e hosts.Entry
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&e); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := hosts.Save(req.Context(), h, e)
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, saved)
	})
	r.Handle("DELETE", "/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		for _, e := range hosts.List(h) {
			if e.ID != id {
				continue
			}
			if err := hosts.Delete(req.Context(), h, e); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		apiserver.Error(w, http.StatusNotFound, "no such host entry")
	})
}
