// Package inboundsmod edits the inbounds of a node's spec over the API: the
// panel's main write path besides clients.
package inboundsmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/inbounds"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "inbounds" }
func (*Module) Tables() []string { return nil } // it writes nodespec, which masterd owns

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/inbounds/list/{node}", New: "/api/v1/inbounds/{node}"},
		{Method: "POST", Old: "/panel/api/inbounds/add/{node}", New: "/api/v1/inbounds/{node}"},
		{Method: "POST", Old: "/panel/api/inbounds/update/{node}/{tag}", New: "/api/v1/inbounds/{node}/{tag}", NewMethod: "PUT"},
		{Method: "POST", Old: "/panel/api/inbounds/del/{node}/{tag}", New: "/api/v1/inbounds/{node}/{tag}", NewMethod: "DELETE"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/{node}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		s, ok := inbounds.Spec(h, req.PathValue("node"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, "no spec for that node")
			return
		}
		apiserver.JSON(w, http.StatusOK, s.Inbounds)
	})
	r.Handle("POST", "/{node}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var in inbounds.Input
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&in); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		ib, err := inbounds.Add(req.Context(), h, h, req.PathValue("node"), in)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, inbounds.ErrExists) {
				code = http.StatusConflict
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusCreated, ib)
	})
	r.Handle("PUT", "/{node}/{tag}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var in inbounds.Input
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&in); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		ib, err := inbounds.Update(req.Context(), h, h, req.PathValue("node"), req.PathValue("tag"), in)
		if err != nil {
			code := http.StatusBadRequest
			switch {
			case errors.Is(err, inbounds.ErrNotFound):
				code = http.StatusNotFound
			case errors.Is(err, inbounds.ErrExists):
				code = http.StatusConflict
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, ib)
	})
	r.Handle("DELETE", "/{node}/{tag}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		err := inbounds.Remove(req.Context(), h, h, req.PathValue("node"), req.PathValue("tag"))
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, inbounds.ErrNotFound) {
				code = http.StatusNotFound
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
