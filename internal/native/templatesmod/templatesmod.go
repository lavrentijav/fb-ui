// Package templatesmod exposes node-spec templates over the API and stamps
// them onto nodes through the same path as any spec write.
package templatesmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templates"
)

// SpecTable is where a node's spec lives (shared with masterd).
const SpecTable = templates.SpecTable

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "templates" }
func (*Module) Tables() []string { return []string{templates.Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/templates/list", New: "/api/v1/templates"},
		{Method: "POST", Old: "/panel/api/templates/add", New: "/api/v1/templates"},
		{Method: "POST", Old: "/panel/api/templates/apply/{name}", New: "/api/v1/templates/{name}/apply"},
		{Method: "POST", Old: "/panel/api/templates/del/{name}", New: "/api/v1/templates/{name}", NewMethod: "DELETE"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, templates.List(h))
	})
	r.Handle("GET", "/{name}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		t, ok := templates.Get(h, req.PathValue("name"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, "no such template")
			return
		}
		apiserver.JSON(w, http.StatusOK, t)
	})
	r.Handle("POST", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body templates.Save
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		t, err := templates.SaveTemplate(req.Context(), h, h, body)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, templates.ErrExists) {
				code = http.StatusConflict
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, t)
	})
	r.Handle("DELETE", "/{name}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if err := templates.Delete(req.Context(), h, req.PathValue("name")); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	r.Handle("POST", "/{name}/apply", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body templates.ApplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := templates.Stamp(req.Context(), h, h, req.PathValue("name"), body)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, templates.ErrNoTemplate) {
				code = http.StatusNotFound
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
}
