// Package groupsmod exposes client groups over the API: the group list with
// its numbers and the bulk operations a group exists for.
package groupsmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/groups"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "groups" }
func (*Module) Tables() []string { return []string{groups.Table} }

// Legacy maps the old panel's group routes, including the shape it expects
// (clientCount/trafficUsed/up/down) and the operations that named the group in
// the body rather than the path.
func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/clients/groups", New: "/api/v1/groups", Wrap: wrapSummaries},
		{Method: "POST", Old: "/panel/api/clients/groups/create", New: "/api/v1/groups"},
		{Method: "POST", Old: "/panel/api/clients/groups/delete", New: "/api/v1/groups", Rewrite: byBodyName("DELETE", "/api/v1/groups/")},
		{Method: "POST", Old: "/panel/api/clients/groups/rename", New: "/api/v1/groups", Rewrite: renameRewrite},
		{Method: "POST", Old: "/panel/api/clients/groups/resetTraffic", New: "/api/v1/groups", Rewrite: byBodyName("POST", "/api/v1/groups/", "/reset")},
		{Method: "GET", Old: "/panel/api/clients/groups/{name}/emails", New: "/api/v1/groups/{name}", Wrap: wrapEmails},
		{Method: "POST", Old: "/panel/api/clients/groups/bulkAdd", New: "/api/v1/groups", Rewrite: bulkAddRewrite},
		{Method: "POST", Old: "/panel/api/clients/groups/bulkRemove", New: "/api/v1/groups/bulk-remove"},
		{Method: "GET", Old: "/panel/api/groups/list", New: "/api/v1/groups", Wrap: wrapSummaries},
		{Method: "POST", Old: "/panel/api/groups/add", New: "/api/v1/groups"},
		{Method: "POST", Old: "/panel/api/groups/del/{name}", New: "/api/v1/groups/{name}", NewMethod: "DELETE"},
	}
}

// wrapSummaries turns the module's Summary into the old page's shape.
func wrapSummaries(body []byte) ([]byte, error) {
	var in []struct {
		Name        string   `json:"name"`
		Emails      []string `json:"emails"`
		TrafficUsed int64    `json:"trafficUsed"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(in))
	for _, g := range in {
		out = append(out, map[string]any{
			"name": g.Name, "clientCount": len(g.Emails), "trafficUsed": g.TrafficUsed,
			"up": int64(0), "down": g.TrafficUsed,
		})
	}
	return json.Marshal(out)
}

// wrapEmails answers the member list the old page expects.
func wrapEmails(body []byte) ([]byte, error) {
	var g struct {
		Emails []string `json:"emails"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, err
	}
	if g.Emails == nil {
		g.Emails = []string{}
	}
	return json.Marshal(g.Emails)
}

// byBodyName rewrites an old POST that names the group in its body into a
// DELETE (or POST) on the group's path.
func byBodyName(method, prefix string, suffix ...string) func(string, string, []byte) (string, string, []byte, error) {
	return func(_, _ string, body []byte) (string, string, []byte, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return "", "", nil, err
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			return "", "", nil, errors.New("a group name is required")
		}
		return method, prefix + url.PathEscape(in.Name) + strings.Join(suffix, ""), nil, nil
	}
}

func renameRewrite(_, _ string, body []byte) (string, string, []byte, error) {
	var in struct {
		OldName string `json:"oldName"`
		NewName string `json:"newName"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return "", "", nil, err
	}
	if strings.TrimSpace(in.OldName) == "" || strings.TrimSpace(in.NewName) == "" {
		return "", "", nil, errors.New("both an old and a new name are required")
	}
	next, _ := json.Marshal(map[string]string{"name": in.NewName})
	return "POST", "/api/v1/groups/" + url.PathEscape(in.OldName) + "/rename", next, nil
}

func bulkAddRewrite(_, _ string, body []byte) (string, string, []byte, error) {
	var in struct {
		Emails []string `json:"emails"`
		Group  string   `json:"group"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return "", "", nil, err
	}
	if strings.TrimSpace(in.Group) == "" {
		return "", "", nil, errors.New("a group name is required")
	}
	next, _ := json.Marshal(map[string]any{"emails": in.Emails, "add": true})
	return "POST", "/api/v1/groups/" + url.PathEscape(in.Group) + "/members", next, nil
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
	r.Handle("POST", "/{name}/rename", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		g, err := rename(req.Context(), h, req.PathValue("name"), body.Name)
		if err != nil {
			fail(w, err)
			return
		}
		apiserver.JSON(w, http.StatusOK, g)
	})
	r.Handle("POST", "/{name}/reset", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		g, ok := groups.Get(h, req.PathValue("name"))
		if !ok {
			fail(w, groups.ErrNoGroup)
			return
		}
		for _, email := range g.Emails {
			if _, err := h.Set(req.Context(), usage.Table, email, map[string]any{"bytes": int64(0)}); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"reset": len(g.Emails)})
	})
	r.Handle("POST", "/bulk-remove", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Emails []string `json:"emails"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		affected := 0
		for _, g := range groups.List(h) {
			members := map[string]bool{}
			for _, e := range g.Emails {
				members[e] = true
			}
			var hit []string
			for _, e := range body.Emails {
				if members[e] {
					hit = append(hit, e)
				}
			}
			if len(hit) == 0 {
				continue
			}
			if _, err := groups.SetMembers(req.Context(), h, g.Name, hit, false); err != nil {
				fail(w, err)
				return
			}
			affected += len(hit)
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"affected": affected})
	})
}

// rename moves a group to a new name, keeping its members and remark.
func rename(ctx context.Context, h module.Host, name, next string) (groups.Group, error) {
	g, ok := groups.Get(h, name)
	if !ok {
		return groups.Group{}, groups.ErrNoGroup
	}
	next = strings.TrimSpace(next)
	if next == "" {
		return groups.Group{}, groups.ErrName
	}
	if next == name {
		return g, nil
	}
	g.Name = next
	saved, err := groups.Save(ctx, h, g, false)
	if err != nil {
		return groups.Group{}, err
	}
	if err := groups.Delete(ctx, h, name); err != nil {
		return groups.Group{}, err
	}
	return saved, nil
}
