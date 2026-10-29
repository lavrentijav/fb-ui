// Package routingmod is traffic routing as a master module: lists of domains,
// IP ranges and ASNs, rules that apply them to nodes, and a tester. The
// masters compile the rules into each node's spec (see routing.Compile).
package routingmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/routing"
)

// Op log tables.
const (
	ListTable = "rlist"
	RuleTable = "rrule"
	// specTable is where masterd keeps each node's spec (masterd.SpecTable).
	specTable = "nodespec"
)

const (
	maxEntries   = 20000
	maxBodyBytes = 32 << 20
)

// refreshEvery is how often the leader looks for lists that are due.
var refreshEvery = time.Minute

type Module struct {
	// Fetch downloads a source URL or an ASN lookup; tests replace it.
	Fetch routing.Fetcher
}

func New() *Module { return &Module{Fetch: httpFetch} }

func (*Module) Name() string     { return "routing" }
func (*Module) Tables() []string { return []string{ListTable, RuleTable} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{
		{Method: "GET", Old: "/panel/api/filters/lists", New: "/api/v1/routing/lists"},
		{Method: "POST", Old: "/panel/api/filters/lists/add", New: "/api/v1/routing/lists"},
		{Method: "POST", Old: "/panel/api/filters/lists/update/{id}", New: "/api/v1/routing/lists/{id}", NewMethod: "PUT"},
		{Method: "POST", Old: "/panel/api/filters/lists/del/{id}", New: "/api/v1/routing/lists/{id}", NewMethod: "DELETE"},
		{Method: "POST", Old: "/panel/api/filters/lists/refresh/{id}", New: "/api/v1/routing/lists/{id}/refresh"},
	}
}

func httpFetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fbui routing list")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("larger than %d MiB", maxBodyBytes>>20)
	}
	return body, nil
}

func str(rec map[string]json.RawMessage, k string) (v string) { _ = json.Unmarshal(rec[k], &v); return }
func num(rec map[string]json.RawMessage, k string) (v int64)  { _ = json.Unmarshal(rec[k], &v); return }
func flag(rec map[string]json.RawMessage, k string) (v bool)  { _ = json.Unmarshal(rec[k], &v); return }
func strs(rec map[string]json.RawMessage, k string) (v []string) {
	_ = json.Unmarshal(rec[k], &v)
	return
}

// Load reads every list and rule in the form routing.Compile wants.
func Load(h module.Host) ([]routing.List, []routing.Rule) {
	var lists []routing.List
	for _, id := range h.Keys(ListTable) {
		rec, ok := h.Get(ListTable, id)
		if !ok {
			continue
		}
		lists = append(lists, routing.List{
			ID: id, Name: str(rec, "name"), Kind: str(rec, "kind"), Entries: strs(rec, "entries"),
			Remote: strs(rec, "remote"), Enable: flag(rec, "enable"),
		})
	}
	var rules []routing.Rule
	for _, id := range h.Keys(RuleTable) {
		rec, ok := h.Get(RuleTable, id)
		if !ok {
			continue
		}
		rules = append(rules, routing.Rule{
			ID: id, Name: str(rec, "name"), Nodes: strs(rec, "nodes"), Inbounds: strs(rec, "inbounds"),
			Lists: strs(rec, "lists"), Action: str(rec, "action"), Order: int(num(rec, "order")), Enable: flag(rec, "enable"),
		})
	}
	return lists, rules
}

type listIn struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Entries      []string `json:"entries"`
	Enable       *bool    `json:"enable"`
	SourceURL    string   `json:"sourceUrl"`
	RefreshHours int      `json:"refreshHours"`
}

func (in *listIn) clean() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("a name is required")
	}
	switch in.Kind {
	case routing.KindIP, routing.KindASN:
	default:
		in.Kind = routing.KindDomain
	}
	in.SourceURL = strings.TrimSpace(in.SourceURL)
	if in.Kind == routing.KindASN {
		in.SourceURL = "" // an ASN list resolves through the prefix lookup, not a URL
	}
	if in.SourceURL != "" && !strings.HasPrefix(in.SourceURL, "http://") && !strings.HasPrefix(in.SourceURL, "https://") {
		return errors.New("source URL must be http or https")
	}
	in.RefreshHours = min(max(in.RefreshHours, 0), 24*30)
	if in.RefreshHours == 0 {
		in.RefreshHours = 24
	}
	seen := map[string]bool{}
	entries := make([]string, 0, len(in.Entries))
	for _, raw := range in.Entries {
		e := strings.TrimSpace(raw)
		if e == "" || strings.HasPrefix(e, "#") {
			continue
		}
		if in.Kind == routing.KindASN {
			asn, err := routing.NormalizeASN(e)
			if err != nil {
				return err
			}
			e = asn
		} else if err := routing.ValidateEntry(in.Kind, e); err != nil {
			return err
		}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	if len(entries) > maxEntries {
		return fmt.Errorf("%d entries, the limit is %d", len(entries), maxEntries)
	}
	in.Entries = entries
	return nil
}

func listView(h module.Host, id string) (map[string]any, bool) {
	rec, ok := h.Get(ListTable, id)
	if !ok {
		return nil, false
	}
	return map[string]any{
		"id": id, "name": str(rec, "name"), "kind": str(rec, "kind"), "entries": strs(rec, "entries"),
		"enable": flag(rec, "enable"), "sourceUrl": str(rec, "sourceUrl"), "refreshHours": num(rec, "refreshHours"),
		"remoteCount": len(strs(rec, "remote")), "fetchedAt": num(rec, "fetchedAt"), "fetchError": str(rec, "fetchError"),
	}, true
}

type ruleIn struct {
	Name     string   `json:"name"`
	Nodes    []string `json:"nodes"`
	Inbounds []string `json:"inbounds"`
	Lists    []string `json:"lists"`
	Action   string   `json:"action"`
	Order    *int     `json:"order"`
	Enable   *bool    `json:"enable"`
}

func (in *ruleIn) clean(h module.Host) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("a name is required")
	}
	if in.Action != nodespec.Direct && in.Action != nodespec.Block && !strings.HasPrefix(in.Action, "proxy_") {
		return errors.New("action must be direct, block or proxy_<country>")
	}
	if len(in.Lists) == 0 {
		return errors.New("a rule needs at least one list")
	}
	for _, id := range in.Lists {
		if _, ok := h.Get(ListTable, id); !ok {
			return fmt.Errorf("list %s not found", id)
		}
	}
	return nil
}

func ruleView(h module.Host, id string) (map[string]any, bool) {
	rec, ok := h.Get(RuleTable, id)
	if !ok {
		return nil, false
	}
	return map[string]any{
		"id": id, "name": str(rec, "name"), "nodes": strs(rec, "nodes"), "inbounds": strs(rec, "inbounds"),
		"lists": strs(rec, "lists"), "action": str(rec, "action"), "order": num(rec, "order"), "enable": flag(rec, "enable"),
	}, true
}

func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		apiserver.Error(w, http.StatusBadRequest, "unreadable body")
		return false
	}
	return true
}

func (m *Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/lists", module.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		out := []map[string]any{}
		for _, id := range h.Keys(ListTable) {
			if v, ok := listView(h, id); ok {
				out = append(out, v)
			}
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	saveList := func(id string, existing map[string]json.RawMessage) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			var in listIn
			if !read(w, req, &in) {
				return
			}
			if err := in.clean(); err != nil {
				apiserver.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			fields := map[string]any{
				"name": in.Name, "kind": in.Kind, "entries": in.Entries, "enable": in.Enable == nil || *in.Enable,
				"sourceUrl": in.SourceURL, "refreshHours": in.RefreshHours,
			}
			// A changed source or entry set must be fetched again at once.
			if existing == nil || str(existing, "sourceUrl") != in.SourceURL || str(existing, "kind") != in.Kind ||
				(in.Kind == routing.KindASN && !slices.Equal(strs(existing, "entries"), in.Entries)) {
				fields["fetchedAt"], fields["remote"], fields["fetchError"] = 0, []string{}, ""
			}
			if _, err := h.Set(req.Context(), ListTable, id, fields); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			v, _ := listView(h, id)
			apiserver.JSON(w, http.StatusOK, v)
		}
	}
	r.Handle("POST", "/lists", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		saveList(h.NextKey(), nil)(w, req)
	})
	r.Handle("PUT", "/lists/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		old, ok := h.Get(ListTable, id)
		if !ok {
			apiserver.Error(w, http.StatusNotFound, "no such list")
			return
		}
		saveList(id, old)(w, req)
	})
	r.Handle("DELETE", "/lists/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(ListTable, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such list")
			return
		}
		_, rules := Load(h)
		var used []string
		for _, rule := range rules {
			if slices.Contains(rule.Lists, id) {
				used = append(used, rule.Name)
			}
		}
		if len(used) > 0 {
			apiserver.Error(w, http.StatusConflict, "list is still used by rule(s): "+strings.Join(used, ", "))
			return
		}
		_, _ = h.Delete(req.Context(), ListTable, id)
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	r.Handle("POST", "/lists/{id}/refresh", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		if _, ok := h.Get(ListTable, id); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such list")
			return
		}
		if err := m.refresh(req.Context(), h, id); err != nil {
			apiserver.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		v, _ := listView(h, id)
		apiserver.JSON(w, http.StatusOK, v)
	})

	r.Handle("GET", "/rules", module.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		out := []map[string]any{}
		for _, id := range h.Keys(RuleTable) {
			if v, ok := ruleView(h, id); ok {
				out = append(out, v)
			}
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	saveRule := func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			var in ruleIn
			if !read(w, req, &in) {
				return
			}
			if err := in.clean(h); err != nil {
				apiserver.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			order := len(h.Keys(RuleTable))
			if in.Order != nil {
				order = *in.Order
			}
			if _, err := h.Set(req.Context(), RuleTable, id, map[string]any{
				"name": in.Name, "nodes": in.Nodes, "inbounds": in.Inbounds, "lists": in.Lists,
				"action": in.Action, "order": order, "enable": in.Enable == nil || *in.Enable,
			}); err != nil {
				apiserver.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			v, _ := ruleView(h, id)
			apiserver.JSON(w, http.StatusOK, v)
		}
	}
	r.Handle("POST", "/rules", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) { saveRule(h.NextKey())(w, req) })
	r.Handle("PUT", "/rules/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if _, ok := h.Get(RuleTable, req.PathValue("id")); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such rule")
			return
		}
		saveRule(req.PathValue("id"))(w, req)
	})
	r.Handle("DELETE", "/rules/{id}", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		if _, ok := h.Get(RuleTable, req.PathValue("id")); !ok {
			apiserver.Error(w, http.StatusNotFound, "no such rule")
			return
		}
		_, _ = h.Delete(req.Context(), RuleTable, req.PathValue("id"))
		apiserver.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	r.Handle("POST", "/test", module.ScopeAdmin, func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Node, Inbound, Target string
			DefaultRoute          string `json:"defaultRoute"`
		}
		if !read(w, req, &in) {
			return
		}
		if strings.TrimSpace(in.Target) == "" {
			apiserver.Error(w, http.StatusBadRequest, "enter a domain or an IP address")
			return
		}
		def := in.DefaultRoute
		if def == "" {
			def = defaultRoute(h, in.Node, in.Inbound)
		}
		lists, rules := Load(h)
		apiserver.JSON(w, http.StatusOK, routing.Test(lists, rules, in.Node, in.Inbound, def, in.Target))
	})
}

// defaultRoute is what an inbound does with traffic no rule names: the Route in
// its node's spec.
func defaultRoute(h module.Host, node, inbound string) string {
	rec, ok := h.Get(specTable, node)
	if !ok {
		return ""
	}
	var spec nodespec.Spec
	if json.Unmarshal(rec["spec"], &spec) != nil {
		return ""
	}
	for _, ib := range spec.Inbounds {
		if ib.Tag == inbound {
			return ib.Route
		}
	}
	return ""
}

// Run refreshes the lists that are due, on the leader.
func (m *Module) Run(ctx context.Context, h module.Host) {
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = m.tick(ctx, h)
		}
	}
}

func (m *Module) tick(ctx context.Context, h module.Host) (int, error) {
	if !h.Leader(ctx) {
		return 0, nil
	}
	var errs []error
	n := 0
	for _, id := range h.Keys(ListTable) {
		rec, ok := h.Get(ListTable, id)
		if !ok || !flag(rec, "enable") || (str(rec, "kind") != routing.KindASN && str(rec, "sourceUrl") == "") {
			continue
		}
		every := time.Duration(max(num(rec, "refreshHours"), 1)) * time.Hour
		if at := num(rec, "fetchedAt"); at != 0 && h.Now().Sub(time.Unix(at, 0)) < every {
			continue
		}
		if err := m.refresh(ctx, h, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", str(rec, "name"), err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// refresh fetches one list. A failed fetch keeps what the list had, so a
// source going down does not silently lift a block.
func (m *Module) refresh(ctx context.Context, h module.Host, id string) error {
	rec, ok := h.Get(ListTable, id)
	if !ok {
		return errors.New("no such list")
	}
	var (
		entries []string
		err     error
	)
	switch kind := str(rec, "kind"); {
	case kind == routing.KindASN:
		entries, _, err = routing.ResolveASN(ctx, m.Fetch, strs(rec, "entries"))
	case str(rec, "sourceUrl") != "":
		var body []byte
		if body, err = m.Fetch(ctx, str(rec, "sourceUrl")); err == nil {
			var skipped int
			if entries, skipped = routing.ParseList(kind, body); len(entries) == 0 {
				err = fmt.Errorf("no usable entries (%d lines skipped)", skipped)
			}
		}
	default:
		return errors.New("this list has no source")
	}
	if err == nil && len(entries) > maxEntries*15 {
		err = fmt.Errorf("%d entries is more than a node can take", len(entries))
	}
	fields := map[string]any{"fetchedAt": h.Now().Unix(), "fetchError": ""}
	if err != nil {
		fields["fetchError"] = err.Error()
	} else {
		fields["remote"] = entries
	}
	if _, serr := h.Set(ctx, ListTable, id, fields); serr != nil {
		return serr
	}
	return err
}
