package masterd

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pool"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/submeta"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// subRender is what one subscription request produces: the links and every
// piece of metadata the response can carry.
type subRender struct {
	token string
	links []string
	info  submeta.Info
}

// renderSub builds a subscription's links and metadata from the projected
// state. It is the single place the response is decided, so the link list, the
// page and the per-server header can never disagree.
func (d *Daemon) renderSub(rec subauth.Record, token string, r *http.Request) subRender {
	up, total, expire := d.emailUsage(rec.Email)
	quota := submeta.Quota{Total: total, Expire: expire}
	for _, n := range d.nodeUsage(rec.Email) {
		quota.Upload += n.Up
		quota.Download += n.Down
	}
	if quota.Used() == 0 {
		quota.Download = up // an older fleet keeps only the combined total
	}

	template := settings.Get(d.store.State, settings.Fleet, "sub.remarkTemplate")
	if strings.TrimSpace(template) == "" {
		// an inbound's remark is the human name; the node-tag fallback below
		// names a plain inbound when it has none
		template = "{{REMARK}}"
	}
	showIdentity := settings.Bool(d.store.State, settings.Fleet, "sub.showIdentity")
	providerID := settings.Get(d.store.State, settings.Fleet, "sub.providerID")

	nodes := d.nodeUsage(rec.Email)
	specs := d.effectiveSpecs()
	// an endpoint's traffic is the sum across every node that advertises it, so a
	// multicast pair reports one total instead of one share per physical node
	endpointUsage := map[string]usage.NodeBytes{}
	for _, s := range specs {
		b, ok := nodes[s.Node]
		if !ok {
			continue
		}
		for _, ib := range s.Inbounds {
			if ib.Host == "" {
				continue
			}
			key := endpointKey(ib)
			sum := endpointUsage[key]
			sum.Up += b.Up
			sum.Down += b.Down
			endpointUsage[key] = sum
		}
	}
	remarks := map[string]string{} // link remark -> endpoint key, for Server-Userinfo
	remark := func(node string, ib nodespec.Inbound) string {
		name := sub.ExpandRemark(template, sub.Vars{
			Node: node, Inbound: ib.Tag, Remark: ib.Remark, Email: rec.Email, User: rec.User,
			Protocol: "vless", Transport: ib.Network,
			Used: quota.Used(), Total: total, Expire: expire,
		})
		if name == "" {
			name = node + "-" + ib.Tag
		}
		if showIdentity && !strings.Contains(name, rec.Email) {
			name += "-" + rec.Email
		}
		remarks[name] = endpointKey(ib)
		return name
	}
	extra := func(node string, ib nodespec.Inbound) map[string]string {
		out := map[string]string{"node": node}
		if providerID != "" {
			out["providerid"] = providerID
		}
		if b := endpointUsage[endpointKey(ib)]; b.Up != 0 || b.Down != 0 {
			// Flamix reads this per link; other clients ignore it
			out["userinfo"] = submeta.Quota{Upload: b.Up, Download: b.Down, Total: total, Expire: expire}.String()
		}
		return out
	}
	links := sub.LinksWithOpts(specs, rec.Email, sub.Options{
		Hosts:  hosts.Endpoints(hosts.List(d.store.State)),
		Remark: remark,
		Extra:  extra,
	})

	info := submeta.Info{
		Title:       d.subTitle(rec),
		UpdateHours: settings.Int(d.store.State, settings.Fleet, "sub.updateHours"),
		SupportURL:  settings.Get(d.store.State, settings.Fleet, "sub.supportURL"),
		WebPageURL:  d.subPageURL(r),
		Announce: submeta.Announce{
			Text:       settings.Get(d.store.State, settings.Fleet, "sub.announce"),
			URL:        settings.Get(d.store.State, settings.Fleet, "sub.announceURL"),
			Color:      settings.Get(d.store.State, settings.Fleet, "sub.announceColor"),
			ButtonText: settings.Get(d.store.State, settings.Fleet, "sub.announceButtonText"),
			ButtonLink: settings.Get(d.store.State, settings.Fleet, "sub.announceButtonLink"),
		},
		Quota:      quota,
		RefillDate: refillDate(settings.Get(d.store.State, settings.Fleet, "sub.refillDate")),
		ProviderID: providerID,
		BackupURLs: settings.List(d.store.State, settings.Fleet, "sub.backupURLs"),
		IPs:        d.siblingIPs(),
		HwidLimit:  hwidLimit(d.store.State, specs, rec.Email),
		Servers:    map[string]submeta.Quota{},
	}
	for name, key := range remarks {
		b := endpointUsage[key]
		if b.Up == 0 && b.Down == 0 {
			continue
		}
		info.Servers[name] = submeta.Quota{
			Upload: b.Up, Download: b.Down, Total: total, Expire: expire,
		}
	}
	return subRender{token: token, links: links, info: info}
}

// endpointKey is the transport identity a multicast pair shares: the same
// public name, path and network is one endpoint even from several nodes.
func endpointKey(ib nodespec.Inbound) string {
	network := ib.Network
	if network == "" {
		network = "ws"
	}
	return ib.Host + "\x00" + network + "\x00" + ib.Path
}

// subTitle is the profile name: the configured one, then the panel name with
// the login, then the login alone.
func (d *Daemon) subTitle(rec subauth.Record) string {
	if t := strings.TrimSpace(settings.Get(d.store.State, settings.Fleet, "sub.title")); t != "" {
		return t
	}
	if name := strings.TrimSpace(settings.Get(d.store.State, settings.Fleet, "panel.name")); name != "" {
		if rec.User != "" {
			return name + " · " + rec.User
		}
		return name
	}
	return rec.User
}

// subPageURL is the subscription's own page, which is what "open site" opens.
func (d *Daemon) subPageURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	return scheme + "://" + host + "/sub/" + r.PathValue("token") + "?format=page"
}

// nodeUsage is every node's share of one email's traffic.
func (d *Daemon) nodeUsage(email string) map[string]usage.NodeBytes {
	out := map[string]usage.NodeBytes{}
	prefix := email + "/"
	for _, key := range d.store.State.Keys(usage.NodeTable) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rec, ok := d.store.State.Get(usage.NodeTable, key)
		if !ok {
			continue
		}
		var b usage.NodeBytes
		_ = json.Unmarshal(rec["up"], &b.Up)
		_ = json.Unmarshal(rec["down"], &b.Down)
		if b.Up != 0 || b.Down != 0 {
			out[strings.TrimPrefix(key, prefix)] = b
		}
	}
	return out
}

// hwidLimit is the strictest per-device cap the subscription's records carry,
// from the client table and from the specs (a spec's client may not have a
// separate record), so a client that honours the Remnawave header enforces the
// limit before the node has to.
func hwidLimit(state module.Reader, specs []nodespec.Spec, email string) int {
	limit := 0
	for _, c := range clients.List(state) {
		if c.Email == email && c.LimitIP > limit {
			limit = c.LimitIP
		}
	}
	for _, s := range specs {
		for _, ib := range s.Inbounds {
			for _, c := range ib.Clients {
				if c.Email == email && c.LimitIP > limit {
					limit = c.LimitIP
				}
			}
		}
	}
	return limit
}

// siblingIPs are the other masters' addresses for the fallback header.
func (d *Daemon) siblingIPs() []string {
	var out []string
	for _, name := range d.store.State.Keys(pool.MastersTable) {
		if name == d.cfg.Name {
			continue
		}
		rec, ok := d.store.State.Get(pool.MastersTable, name)
		if !ok {
			continue
		}
		var ips []string
		_ = json.Unmarshal(rec["ips"], &ips)
		out = append(out, ips...)
	}
	return out
}

// refillDate parses the operator's reset date (YYYY-MM-DD or RFC 3339).
func refillDate(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix()
	}
	return 0
}

// subFormat is the body format a request asked for.
func subFormat(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
}

// writeSubHeaders sets every metadata header, and the no-cache pair clients
// need to refresh a subscription immediately.
func writeSubHeaders(w http.ResponseWriter, info submeta.Info) {
	for k, v := range info.Headers() {
		w.Header().Set(k, v)
	}
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
}
