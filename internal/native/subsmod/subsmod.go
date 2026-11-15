// Package subsmod is the operator's subscription surface as a module: it lists
// subscriptions with their login and links, issues and revokes them, and shows
// the virtual networks subscribers run. It reads the same op-log tables the
// rest of the fleet writes, so it works on any master and behind the panel's
// session cookie.
package subsmod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subparam"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

// SpecTable is where each node's spec lives (masterd.SpecTable).
const SpecTable = "nodespec"

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string { return "subs" }
func (*Module) Tables() []string {
	return []string{subauth.Table, subparam.Table, vnet.NetTable, vnet.MemberTable}
}

// Legacy is the old panel's surface the subscription page calls, so the page
// works unchanged against a native master through /panel/api.
func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/subscriptions/list", New: "/api/v1/subs"},
		{Method: "POST", Old: "/panel/api/subscriptions/create", New: "/api/v1/subs", Rewrite: createRewrite},
		{Method: "POST", Old: "/panel/api/subscriptions/password/{email}", New: "/api/v1/subs/{email}/password"},
		{Method: "POST", Old: "/panel/api/subscriptions/enable/{token}", New: "/api/v1/subs/{token}/enable"},
		{Method: "POST", Old: "/panel/api/subscriptions/del/{token}", New: "/api/v1/subs/{token}", NewMethod: "DELETE"},
		{Method: "GET", Old: "/panel/api/vnets/list", New: "/api/v1/subs/networks"},
	}
}

// createRewrite turns the page's {email,user,password} into a PUT on the
// email's path, since the old route carried the email in the body.
func createRewrite(_, _ string, body []byte) (string, string, []byte, error) {
	var in struct {
		Email    string `json:"email"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return "", "", nil, err
	}
	in.Email = strings.TrimSpace(in.Email)
	if in.Email == "" {
		return "", "", nil, errors.New("an email is required")
	}
	next, _ := json.Marshal(map[string]string{"user": in.User, "password": in.Password})
	return "PUT", "/api/v1/subs/" + pathEscape(in.Email), next, nil
}

// pathEscape escapes one path segment without turning a space into "+".
func pathEscape(s string) string {
	return strings.ReplaceAll(urlEncode(s), "+", "%20")
}

func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%")
		const hexDigits = "0123456789ABCDEF"
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// subscriptionView is one row of the operator's list.
type subscriptionView struct {
	Token      string          `json:"token"`
	Email      string          `json:"email"`
	User       string          `json:"user,omitempty"`
	Enable     bool            `json:"enable"`
	Links      int             `json:"links"`
	UsedBytes  int64           `json:"usedBytes"`
	TotalBytes int64           `json:"totalBytes"`
	Expire     int64           `json:"expire"`
	URL        string          `json:"url"`
	Params     subparam.Params `json:"params,omitempty"`
	Aliases    []string        `json:"aliases,omitempty"`
}

func str(rec map[string]json.RawMessage, k string) (v string) { _ = json.Unmarshal(rec[k], &v); return }

func num(rec map[string]json.RawMessage, k string) (v int64) { _ = json.Unmarshal(rec[k], &v); return }

func flag(rec map[string]json.RawMessage, k string) (v bool) { _ = json.Unmarshal(rec[k], &v); return }

// specs reads every node's spec and applies what modules add at render time,
// so the link count matches what /sub actually serves.
func specs(h module.Reader) []nodespec.Spec {
	mods := module.All()
	var out []nodespec.Spec
	for _, node := range h.Keys(SpecTable) {
		rec, ok := h.Get(SpecTable, node)
		var s nodespec.Spec
		if !ok || json.Unmarshal(rec["spec"], &s) != nil {
			continue
		}
		for _, m := range mods {
			if c, ok := m.(module.SpecContributor); ok {
				c.ContributeSpec(h, s.Node, &s)
			}
		}
		out = append(out, s)
	}
	return out
}

// usage answers how much a client used and its limit and expiry.
func usageOf(r module.Reader, email string) (used, total, expire int64) {
	if rec, ok := r.Get(usage.Table, email); ok {
		used = num(rec, "bytes")
	}
	for _, c := range clients.List(r) {
		if c.Email != email {
			continue
		}
		if c.TotalGB > total {
			total = c.TotalGB
		}
		if c.ExpiresAt != 0 && (expire == 0 || c.ExpiresAt < expire) {
			expire = c.ExpiresAt / 1000
		}
	}
	return
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		out := make([]subscriptionView, 0, len(h.Keys(subauth.Table)))
		sp := specs(h)
		endpoints := hosts.Endpoints(hosts.List(h))
		for _, token := range h.Keys(subauth.Table) {
			rec, ok := subauth.Get(h, token)
			if !ok || rec.AliasOf != "" {
				continue
			}
			params, _ := subparam.Get(h, token)
			used, total, expire := usageOf(h, rec.Email)
			out = append(out, subscriptionView{
				Token: token, Email: rec.Email, User: rec.User, Enable: rec.Enable,
				Links:     len(sub.LinksWith(sp, rec.Email, endpoints)),
				UsedBytes: used, TotalBytes: total, Expire: expire, URL: "/sub/" + token,
				Params: params, Aliases: subauth.Aliases(h, token),
			})
		}
		slices.SortFunc(out, func(a, b subscriptionView) int { return strings.Compare(a.Email, b.Email) })
		apiserver.JSON(w, http.StatusOK, out)
	})

	// Custom parameters are keyed by the subscription token, not the email:
	// one email has one subscription, but the token is what the bot stores.
	r.Handle("GET", "/{token}/params", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := subauth.Get(h, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		params, _ := subparam.Get(h, token)
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "params": params})
	})
	r.Handle("PUT", "/{token}/params", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := subauth.Get(h, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		var body struct {
			Params subparam.Params `json:"params"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := subparam.Set(req.Context(), h, token, body.Params); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "params": body.Params})
	})
	r.Handle("POST", "/{token}/params", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := subauth.Get(h, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		var body struct {
			Params subparam.Params `json:"params"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		params, err := subparam.Merge(req.Context(), h, token, body.Params)
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "params": params})
	})
	r.Handle("DELETE", "/{token}/params/{key}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := subauth.Get(h, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		params, err := subparam.DeleteKey(req.Context(), h, token, req.PathValue("key"))
		if err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "params": params})
	})
	r.Handle("DELETE", "/{token}/params", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := subauth.Get(h, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		if err := subparam.Delete(req.Context(), h, token); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "params": subparam.Params{}})
	})

	r.Handle("PUT", "/{email}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		email := req.PathValue("email")
		if strings.TrimSpace(email) == "" {
			apiserver.Error(w, http.StatusBadRequest, "an email is required")
			return
		}
		var body struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(io.LimitReader(req.Body, 1<<12)).Decode(&body)
		if body.User != "" && !subauth.ValidUser(body.User) {
			apiserver.Error(w, http.StatusBadRequest, subauth.ErrUser.Error())
			return
		}
		existing, have := byEmail(h, email)
		pass := ""
		switch {
		case body.Password != "":
			hash, err := subauth.Password(body.Password)
			if err != nil {
				apiserver.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			pass = hash
		case have && existing.PassHash != "":
		default:
			gen, err := subauth.NewPassword()
			if err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			hash, err := subauth.Password(gen)
			if err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			pass, body.Password = hash, gen
		}
		token, user, err := save(req.Context(), h, email, body.User, pass)
		if err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := map[string]any{"token": token, "email": email, "user": user, "url": "/sub/" + token}
		if body.Password != "" {
			out["password"] = body.Password // shown once
		}
		apiserver.JSON(w, http.StatusOK, out)
	})

	// Aliases are extra public tokens for one subscription; the old panel's
	// subId becomes an alias during import, so old links keep resolving.
	r.Handle("GET", "/{token}/aliases", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		rec, ok := subauth.Get(h, token)
		if !ok || rec.AliasOf != "" {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "aliases": subauth.Aliases(h, token)})
	})
	r.Handle("POST", "/{token}/aliases", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		rec, ok := subauth.Get(h, token)
		if !ok || rec.AliasOf != "" {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		var body struct {
			Alias  string `json:"alias"`
			Enable *bool  `json:"enable"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		enable := true
		if body.Enable != nil {
			enable = *body.Enable
		}
		if err := subauth.EnsureAlias(req.Context(), h, body.Alias, token, enable); err != nil {
			code := http.StatusBadRequest
			switch {
			case errors.Is(err, subauth.ErrAliasTaken):
				code = http.StatusConflict
			case errors.Is(err, subauth.ErrNoSubscription):
				code = http.StatusNotFound
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "alias": body.Alias, "email": rec.Email, "enable": enable})
	})
	r.Handle("DELETE", "/{token}/aliases/{alias}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if err := subauth.DeleteAlias(req.Context(), h, req.PathValue("alias"), token); err != nil {
			code := http.StatusNotFound
			if errors.Is(err, subauth.ErrNoSubscription) {
				code = http.StatusNotFound
			}
			apiserver.Error(w, code, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": token, "alias": req.PathValue("alias"), "deleted": true})
	})

	r.Handle("POST", "/{email}/password", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		rec, ok := byEmail(h, req.PathValue("email"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		gen, err := subauth.NewPassword()
		if err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		hash, err := subauth.Password(gen)
		if err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		fields := fieldsOf(rec)
		fields["pass"] = hash
		if _, err := h.Set(req.Context(), subauth.Table, rec.Token, fields); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": rec.Token, "user": rec.User, "password": gen})
	})

	r.Handle("POST", "/{token}/enable", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		rec, ok := subauth.Get(h, req.PathValue("token"))
		if !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		var body struct {
			Enable bool `json:"enable"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<12)).Decode(&body); err != nil {
			apiserver.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		fields := fieldsOf(rec)
		fields["enable"] = body.Enable
		if _, err := h.Set(req.Context(), subauth.Table, rec.Token, fields); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]any{"token": rec.Token, "enable": body.Enable})
	})

	r.Handle("DELETE", "/{token}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		token := req.PathValue("token")
		if _, ok := h.Get(subauth.Table, token); !ok {
			apiserver.Error(w, http.StatusNotFound, subauth.ErrNoSubscription.Error())
			return
		}
		if err := subparam.Delete(req.Context(), h, token); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := h.Delete(req.Context(), subauth.Table, token); err != nil {
			apiserver.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})

	r.Handle("GET", "/networks", module.ScopeMonitor, func(w http.ResponseWriter, _ *http.Request) {
		out := make([]map[string]any, 0, len(h.Keys(vnet.NetTable)))
		for _, id := range h.Keys(vnet.NetTable) {
			rec, ok := h.Get(vnet.NetTable, id)
			if !ok {
				continue
			}
			members := make([]map[string]any, 0)
			for _, user := range h.Keys(vnet.MemberTable) {
				mrec, ok := h.Get(vnet.MemberTable, user)
				if !ok || str(mrec, "network") != id {
					continue
				}
				members = append(members, map[string]any{
					"user": user, "name": nameOf(h, user), "ip": addrOf(h, user), "role": str(mrec, "role"),
				})
			}
			slices.SortFunc(members, func(a, b map[string]any) int {
				return strings.Compare(a["user"].(string), b["user"].(string))
			})
			out = append(out, map[string]any{
				"id": id, "title": str(rec, "title"), "owner": str(rec, "owner"), "members": members,
			})
		}
		slices.SortFunc(out, func(a, b map[string]any) int {
			return strings.Compare(a["id"].(string), b["id"].(string))
		})
		apiserver.JSON(w, http.StatusOK, out)
	})
}

func nameOf(h module.Reader, user string) string {
	for _, name := range h.Keys(vnet.NameTable) {
		if rec, ok := h.Get(vnet.NameTable, name); ok && str(rec, "user") == user {
			return name
		}
	}
	return ""
}

func addrOf(h module.Reader, user string) string {
	if rec, ok := h.Get(vnet.UserTable, user); ok {
		return str(rec, "ip")
	}
	return ""
}

func fieldsOf(rec subauth.Record) map[string]any {
	out := map[string]any{"email": rec.Email, "user": rec.User, "pass": rec.PassHash, "enable": rec.Enable}
	if rec.AliasOf != "" {
		out["aliasOf"] = rec.AliasOf
	}
	return out
}

// byEmail finds the subscription with this email.
func byEmail(r module.Reader, email string) (subauth.Record, bool) {
	for _, token := range r.Keys(subauth.Table) {
		rec, ok := subauth.Get(r, token)
		if ok && rec.Email == email && rec.AliasOf == "" {
			return rec, true
		}
	}
	return subauth.Record{}, false
}

// save writes the subscription, keeping the existing token and login (and the
// enabled flag) when only the password changes.
func save(ctx context.Context, h module.Host, email, user, passHash string) (token, outUser string, err error) {
	if rec, ok := byEmail(h, email); ok {
		token = rec.Token
	}
	if token == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return "", "", err
		}
		token = hex.EncodeToString(buf)
	}
	fields := map[string]any{"email": email, "enable": true}
	if rec, ok := h.Get(subauth.Table, token); ok {
		fields["enable"] = flag(rec, "enable")
		if u := str(rec, "user"); u != "" {
			fields["user"] = u
		}
		if p := str(rec, "pass"); p != "" {
			fields["pass"] = p
		}
	}
	if user != "" {
		fields["user"] = user
	} else if _, ok := fields["user"]; !ok {
		fields["user"] = email
	}
	if passHash != "" {
		fields["pass"] = passHash
	}
	if _, err := h.Set(ctx, subauth.Table, token, fields); err != nil {
		return "", "", err
	}
	outUser, _ = fields["user"].(string)
	return token, outUser, nil
}

// Run keeps nothing alive.
func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }
