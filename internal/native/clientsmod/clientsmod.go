// Package clientsmod exposes the node inbounds' clients over the API and
// adds the live ones to a node's spec when a master renders it.
package clientsmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "clients" }
func (*Module) Tables() []string { return []string{clients.Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/clients/list", New: "/api/v1/clients"},
		{Method: "POST", Old: "/panel/api/clients/add", New: "/api/v1/clients"},
		{Method: "POST", Old: "/panel/api/clients/del/{node}/{inbound}/{email}", New: "/api/v1/clients/{node}/{inbound}/{email}", NewMethod: "DELETE"},
		{Method: "GET", Old: "/panel/api/clients/get/{email}", New: "/api/v1/clients/by-email/{email}"},
		{Method: "POST", Old: "/panel/api/clients/update/{email}", New: "/api/v1/clients/by-email/{email}", NewMethod: "PUT"},
		{Method: "POST", Old: "/panel/api/clients/del/{email}", New: "/api/v1/clients/by-email/{email}", NewMethod: "DELETE"},
		{Method: "GET", Old: "/panel/api/clients/onlines", New: "/api/v1/clients/onlines"},
	}
}

func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }

// ContributeSpec adds this node's active clients to its inbounds.
func (*Module) ContributeSpec(r module.Reader, node string, spec *nodespec.Spec) {
	clients.Attach(r, node, spec, time.Now())
}

// input is the wire shape; expiry accepts RFC3339 or unix ms.
type input struct {
	clients.Client
	Expiry string `json:"expiry"`
}

func (in input) client() (clients.Client, error) {
	c := in.Client
	if in.Expiry != "" {
		ms, err := clients.ParseExpiry(in.Expiry)
		if err != nil {
			return clients.Client{}, err
		}
		c.ExpiresAt = ms
	}
	return c, nil
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, clients.List(h))
	})
	r.Handle("GET", "/{node}/{inbound}/{email}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		c, ok := clients.Get(h, req.PathValue("node"), req.PathValue("inbound"), req.PathValue("email"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, clients.ErrNoClient.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, c)
	})
	r.Handle("POST", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var in input
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&in); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		c, err := in.client()
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, created, err := clients.Save(req.Context(), h, c)
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		apiserver.JSON(w, code, saved)
	})
	// create without replacing: a duplicate email on the same inbound is refused
	r.Handle("PUT", "/", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var in input
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&in); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		c, err := in.client()
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := clients.Create(req.Context(), h, c)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, clients.ErrExists) {
				code = http.StatusConflict
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusCreated, saved)
	})
	r.Handle("DELETE", "/{node}/{inbound}/{email}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if err := clients.Delete(req.Context(), h, req.PathValue("node"), req.PathValue("inbound"), req.PathValue("email")); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// A bot key is one email across nodes and inbounds; the old panel's client
	// form addresses it by email too, so these routes fan out.
	byEmail := func(email string) []clients.Client {
		var out []clients.Client
		for _, c := range clients.List(h) {
			if c.Email == email {
				out = append(out, c)
			}
		}
		return out
	}

	r.Handle("GET", "/by-email/{email}", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		recs := byEmail(req.PathValue("email"))
		if len(recs) == 0 {
			apiserver.Error(w, http.StatusNotFound, clients.ErrNoClient.Error())
			return
		}
		c := recs[0]
		apiserver.JSON(w, http.StatusOK, map[string]any{
			"client": map[string]any{
				"id": 0, "email": c.Email, "uuid": c.UUID, "flow": "", "subId": "",
				"enable": c.Enable, "totalGB": c.TotalGB, "expiryTime": c.ExpiresAt, "limitIp": c.LimitIP,
			},
			"inboundIds": []int{},
		})
	})
	r.Handle("PUT", "/by-email/{email}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Enable     *bool  `json:"enable"`
			TotalGB    *int64 `json:"totalGB"`
			ExpiryTime *int64 `json:"expiryTime"`
			LimitIP    *int   `json:"limitIp"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		recs := byEmail(req.PathValue("email"))
		if len(recs) == 0 {
			apiserver.Error(w, http.StatusNotFound, clients.ErrNoClient.Error())
			return
		}
		for _, c := range recs {
			if body.Enable != nil {
				c.Enable = *body.Enable
			}
			if body.TotalGB != nil {
				c.TotalGB = *body.TotalGB
			}
			if body.ExpiryTime != nil {
				c.ExpiresAt = *body.ExpiryTime
			}
			if body.LimitIP != nil {
				c.LimitIP = *body.LimitIP
			}
			if _, _, err := clients.Save(req.Context(), h, c); err != nil {
				apiserver.Error(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"updated": len(recs)})
	})
	r.Handle("DELETE", "/by-email/{email}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		recs := byEmail(req.PathValue("email"))
		for _, c := range recs {
			if err := clients.Delete(req.Context(), h, c.Node, c.Inbound, c.Email); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"deleted": len(recs)})
	})
	// The native master keeps no per-IP online table; the page tolerates empty.
	r.Handle("GET", "/onlines", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		apiserver.JSON(w, http.StatusOK, []any{})
	})
}
