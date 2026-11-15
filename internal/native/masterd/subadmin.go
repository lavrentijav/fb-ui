package masterd

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

// subscriptionView is one row of the operator's subscription list.
type subscriptionView struct {
	Token      string   `json:"token"`
	Email      string   `json:"email"`
	User       string   `json:"user,omitempty"`
	Enable     bool     `json:"enable"`
	Links      int      `json:"links"`
	UsedBytes  int64    `json:"usedBytes"`
	TotalBytes int64    `json:"totalBytes"`
	Expire     int64    `json:"expire"`
	URL        string   `json:"url"`
	Aliases    []string `json:"aliases,omitempty"`
}

// listSubscriptions answers every subscription with its login, link count and
// traffic, so an operator can issue and revoke them.
func (d *Daemon) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	specs := d.effectiveSpecs()
	endpoints := hosts.Endpoints(hosts.List(d.store.State))
	out := make([]subscriptionView, 0, len(d.store.State.Keys(subauth.Table)))
	for _, token := range d.store.State.Keys(subauth.Table) {
		rec, ok := subauth.Get(d.store.State, token)
		if !ok || rec.AliasOf != "" {
			continue
		}
		used, total, expire := d.emailUsage(rec.Email)
		out = append(out, subscriptionView{
			Token:      token,
			Email:      rec.Email,
			User:       rec.User,
			Enable:     rec.Enable,
			Links:      len(sub.LinksWith(specs, rec.Email, endpoints)),
			UsedBytes:  used,
			TotalBytes: total,
			Expire:     expire,
			URL:        "/sub/" + token,
			Aliases:    subauth.Aliases(d.store.State, token),
		})
	}
	slices.SortFunc(out, func(a, b subscriptionView) int { return strings.Compare(a.Email, b.Email) })
	apiserver.JSON(w, http.StatusOK, out)
}

// setSubEnabled turns a subscription's link list on or off without touching
// its login or password.
func (d *Daemon) setSubEnabled(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")
	rec, ok := subauth.Get(d.store.State, token)
	if !ok {
		http.Error(w, subauth.ErrNoSubscription.Error(), http.StatusNotFound)
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fields := map[string]any{"email": rec.Email, "enable": body.Enable}
	if rec.User != "" {
		fields["user"] = rec.User
	}
	if rec.PassHash != "" {
		fields["pass"] = rec.PassHash
	}
	if rec.AliasOf != "" {
		fields["aliasOf"] = rec.AliasOf
	}
	if _, err := m.Set(r.Context(), SubTokenTable, token, fields); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "enable": body.Enable})
}

// networkView is one virtual network as the operator sees it.
type networkView struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Owner   string       `json:"owner"`
	Members []memberView `json:"members"`
}

type memberView struct {
	User string `json:"user"`
	Name string `json:"name,omitempty"`
	IP   string `json:"ip,omitempty"`
	Role string `json:"role"`
}

// listNetworks answers the virtual networks and their members. An operator
// only observes them here; subscribers manage their own through /api/v1/sub.
func (d *Daemon) listNetworks(w http.ResponseWriter, r *http.Request) {
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
	s := d.vnets.Load()
	if s == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	out := make([]networkView, 0, len(s.Networks()))
	for _, id := range s.Networks() {
		rec, _ := s.State.Get(vnet.NetTable, id)
		var title, owner string
		_ = json.Unmarshal(rec["title"], &title)
		_ = json.Unmarshal(rec["owner"], &owner)
		n := networkView{ID: id, Title: title, Owner: owner, Members: []memberView{}}
		for _, m := range s.Members(id) {
			u, _ := s.User(m.User)
			n.Members = append(n.Members, memberView{
				User: m.User, Name: s.Name(m.User), IP: u.IP, Role: m.Role,
			})
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b networkView) int { return strings.Compare(a.ID, b.ID) })
	apiserver.JSON(w, http.StatusOK, out)
}
