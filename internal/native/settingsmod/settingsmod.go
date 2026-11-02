// Package settingsmod exposes the master's typed settings over the API. A
// secret is never echoed back; saving an empty secret keeps the stored one.
package settingsmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "settings" }
func (*Module) Tables() []string { return []string{settings.Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/settings/all", New: "/api/v1/settings"},
		{Method: "POST", Old: "/panel/api/settings/update", New: "/api/v1/settings"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, settings.All(h, settings.Fleet))
	})
	// one value, useful to a script that only needs the port or the panel name
	r.Handle("GET", "/{name}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if _, ok := settings.Fleet[name]; !ok {
			apiserver.Error(w, http.StatusNotFound, settings.ErrUnknownKey.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, settings.Get(h, settings.Fleet, name))
	})
	r.Handle("PUT", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := settings.SetMany(req.Context(), h, settings.Fleet, body); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, settings.ErrUnknownKey) {
				code = http.StatusNotFound
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, settings.All(h, settings.Fleet))
	})
}
