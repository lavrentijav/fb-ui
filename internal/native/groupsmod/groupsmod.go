// Package groupsmod exposes client groups over the API: the group list with
// its numbers and the bulk operations a group exists for.
package groupsmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/groups"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "groups" }
func (*Module) Tables() []string { return []string{groups.Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/groups/list", New: "/api/v1/groups"},
		{Method: "POST", Old: "/panel/api/groups/add", New: "/api/v1/groups"},
		{Method: "POST", Old: "/panel/api/groups/del/{name}", New: "/api/v1/groups/{name}", NewMethod: "DELETE"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, groups.ErrNoGroup):
		code = http.StatusNotFound
	case errors.Is(err, groups.ErrExists):
		code = http.StatusConflict
	}
	apiserver.Error(w, code, err.Error())
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, groups.Summaries(h, h.Now()))
	})
	r.Handle("GET", "/{name}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		g, ok := groups.Get(h, req.PathValue("name"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, groups.ErrNoGroup.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, groups.Summarize(h, g, h.Now()))
	})
	r.Handle("POST", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Name      string   `json:"name"`
			Remark    string   `json:"remark"`
			Emails    []string `json:"emails"`
			Overwrite bool     `json:"overwrite"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		g, err := groups.Save(req.Context(), h, groups.Group{Name: body.Name, Remark: body.Remark, Emails: body.Emails}, body.Overwrite)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, g)
	})
	r.Handle("DELETE", "/{name}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if err := groups.Delete(req.Context(), h, req.PathValue("name")); err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	r.Handle("POST", "/{name}/members", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Emails []string `json:"emails"`
			Add    bool     `json:"add"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		g, err := groups.SetMembers(req.Context(), h, req.PathValue("name"), body.Emails, body.Add)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, g)
	})
	r.Handle("POST", "/{name}/enable", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Enable bool `json:"enable"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body)
		n, err := groups.SetEnabled(req.Context(), h, req.PathValue("name"), body.Enable)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"changed": n})
	})
	r.Handle("POST", "/{name}/attach", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Node    string `json:"node"`
			Inbound string `json:"inbound"`
			UUID    string `json:"uuid"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		n, err := groups.Attach(req.Context(), h, req.PathValue("name"), body.Node, body.Inbound, body.UUID)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"created": n})
	})
	r.Handle("POST", "/{name}/detach", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Node    string `json:"node"`
			Inbound string `json:"inbound"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		n, err := groups.Detach(req.Context(), h, req.PathValue("name"), body.Node, body.Inbound)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"removed": n})
	})
}
