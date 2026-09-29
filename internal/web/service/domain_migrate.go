package service

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// Domain migration modes. Add puts every name under From also under To;
// switch does that and then points nodes, hosts and inbounds at the new names.
const (
	DomainMigrateAdd    = "add"
	DomainMigrateSwitch = "switch"
)

// DomainMigrateRequest moves (or copies) every name under one base domain to
// another: ru7.walk.old.com becomes ru7.walk.new.com, and so do pools.
type DomainMigrateRequest struct {
	From string `json:"from" example:"walk.boykisser.online"`
	To   string `json:"to" example:"walk.example.com"`
	Mode string `json:"mode" example:"add"`
	// NodeIds limits the per-node work (edge routes, addresses, inbounds);
	// empty means every node. 0 is this panel.
	NodeIds []int `json:"nodeIds" example:"[3,4]"`
}

// DomainMigrateAction is one change the migration makes, planned or done.
type DomainMigrateAction struct {
	Kind   string `json:"kind" example:"dns"`
	Target string `json:"target" example:"ru7.walk.example.com"`
	Detail string `json:"detail" example:"A 203.0.113.7"`
	Done   bool   `json:"done" example:"false"`
	Error  string `json:"error" example:""`
}

// migrateLookupIP reads the addresses a name has now; tests replace it.
var migrateLookupIP = net.DefaultResolver.LookupIP

type DomainMigrateService struct {
	dns   DnsProviderService
	certs CertificateService
}

// domainRewriter renames whole names under from; a name inside a file path
// (/root/cert/<name>/...) is left alone, the files keep their old place.
type domainRewriter struct {
	from, to string
	re       *regexp.Regexp
}

func newDomainRewriter(from, to string) *domainRewriter {
	return &domainRewriter{
		from: from, to: to,
		re: regexp.MustCompile(`(?i)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*` + regexp.QuoteMeta(from)),
	}
}

func isNameByte(c byte) bool {
	return c == '-' || c == '/' || c == '.' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// spans are the standalone matches: not glued to a longer label, not a
// parent of a longer name (old.com.evil.net) and not part of a file path;
// the host of a URL counts.
func (r *domainRewriter) spans(text string) [][]int {
	var out [][]int
	for _, m := range r.re.FindAllStringIndex(text, -1) {
		// https://name/path is a URL host, not a file path.
		inURL := m[0] >= 3 && text[m[0]-3:m[0]] == "://"
		if m[0] > 0 && isNameByte(text[m[0]-1]) && !inURL {
			continue
		}
		if m[1] < len(text) {
			next := text[m[1]]
			if inURL && (next == '/' || next == '?' || next == '#') {
				out = append(out, m)
				continue
			}
			if next == '.' {
				if m[1]+1 < len(text) && isNameByte(text[m[1]+1]) {
					continue
				}
			} else if isNameByte(next) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// names finds the names under from in text.
func (r *domainRewriter) names(text string) []string {
	var out []string
	for _, m := range r.spans(text) {
		if name := strings.ToLower(text[m[0]:m[1]]); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func (r *domainRewriter) rename(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), r.from) + r.to
}

// rewrite renames every standalone name under from in text.
func (r *domainRewriter) rewrite(text string) string {
	var b strings.Builder
	last := 0
	for _, m := range r.spans(text) {
		b.WriteString(text[last:m[0]])
		b.WriteString(r.rename(text[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

func (s *DomainMigrateService) normalize(req *DomainMigrateRequest) error {
	req.From = dnsprovider.NormalizeName(req.From)
	req.To = dnsprovider.NormalizeName(req.To)
	if req.Mode == "" {
		req.Mode = DomainMigrateAdd
	}
	switch {
	case req.Mode != DomainMigrateAdd && req.Mode != DomainMigrateSwitch:
		return common.NewErrorf("mode %q: use add or switch", req.Mode)
	case !strings.Contains(req.From, ".") || !strings.Contains(req.To, "."):
		return common.NewError("both domains look like example.com")
	case req.From == req.To || dnsprovider.InZone(req.To, req.From) || dnsprovider.InZone(req.From, req.To):
		return common.NewError("the new domain must not be the old one or inside it")
	}
	if _, _, err := s.dns.ForName("x." + req.To); err != nil {
		return common.NewErrorf("no DNS provider here manages %s", req.To)
	}
	return nil
}

func (req *DomainMigrateRequest) coversNode(id int) bool {
	return len(req.NodeIds) == 0 || slices.Contains(req.NodeIds, id)
}

type domainMigration struct {
	req     *DomainMigrateRequest
	rw      *domainRewriter
	actions []DomainMigrateAction
	apply   []func() error
}

func (m *domainMigration) add(kind, target, detail string, fn func() error) {
	m.actions = append(m.actions, DomainMigrateAction{Kind: kind, Target: target, Detail: detail})
	m.apply = append(m.apply, fn)
}

// Plan lists what Apply would change, without changing anything.
func (s *DomainMigrateService) Plan(req *DomainMigrateRequest) ([]DomainMigrateAction, error) {
	m, err := s.build(req)
	if err != nil {
		return nil, err
	}
	return m.actions, nil
}

// Apply makes the changes in order: records first, then certificates and
// edge routes, and only then the switches that depend on them.
func (s *DomainMigrateService) Apply(req *DomainMigrateRequest) ([]DomainMigrateAction, error) {
	m, err := s.build(req)
	if err != nil {
		return nil, err
	}
	for i, fn := range m.apply {
		if err := fn(); err != nil {
			m.actions[i].Error = err.Error()
			continue
		}
		m.actions[i].Done = true
	}
	return m.actions, nil
}

func (s *DomainMigrateService) build(req *DomainMigrateRequest) (*domainMigration, error) {
	if err := s.normalize(req); err != nil {
		return nil, err
	}
	m := &domainMigration{req: req, rw: newDomainRewriter(req.From, req.To)}
	db := database.GetDB()

	var nodes []*model.Node
	if err := db.Order("id asc").Find(&nodes).Error; err != nil {
		return nil, err
	}
	var hosts []*model.Host
	if err := db.Order("id asc").Find(&hosts).Error; err != nil {
		return nil, err
	}
	certs, err := s.certs.List()
	if err != nil {
		return nil, err
	}
	var inbounds []*model.Inbound
	if err := db.Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}

	var names []string
	collect := func(texts ...string) {
		for _, t := range texts {
			for _, n := range m.rw.names(t) {
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
	}
	for _, n := range nodes {
		if n.Role == model.NodeRoleNode && m.req.coversNode(n.Id) {
			collect(n.Address)
		}
	}
	for _, h := range hosts {
		collect(h.Address, h.Sni, h.HostHeader)
	}
	for _, c := range certs {
		collect(strings.Join(c.Domains, " "))
	}
	slices.Sort(names)

	for _, name := range names {
		s.planRecord(m, name)
	}
	for _, c := range certs {
		s.planCert(m, c)
	}
	s.planEdge(m, nodes)
	if req.Mode == DomainMigrateSwitch {
		s.planSwitch(m, nodes, hosts, inbounds)
	}
	return m, nil
}

func (s *DomainMigrateService) planRecord(m *domainMigration, name string) {
	target := m.rw.rename(name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ips, err := migrateLookupIP(ctx, "ip", strings.TrimPrefix(name, "*."))
	cancel()
	if err != nil || len(ips) == 0 || strings.HasPrefix(name, "*.") {
		m.actions = append(m.actions, DomainMigrateAction{Kind: "dns", Target: target, Error: fmt.Sprintf("%s does not resolve; add its record by hand", name)})
		m.apply = append(m.apply, func() error { return fmt.Errorf("%s does not resolve", name) })
		return
	}
	byType := map[string][]string{}
	for _, ip := range ips {
		t := "A"
		if ip.To4() == nil {
			t = "AAAA"
		}
		byType[t] = append(byType[t], ip.String())
	}
	for _, t := range []string{"A", "AAAA"} {
		values := byType[t]
		if len(values) == 0 {
			continue
		}
		slices.Sort(values)
		m.add("dns", target, t+" "+strings.Join(values, ", "), func() error {
			prov, _, err := s.dns.ForName(target)
			if err != nil {
				return err
			}
			client, zone, err := s.dns.ClientFor(prov.Id, target)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := client.SetRecords(ctx, target, t, values, 300); err != nil {
				return err
			}
			return dnsWaitVisible(ctx, zone, target, t, values, 2*time.Minute)
		})
	}
}

func (s *DomainMigrateService) planCert(m *domainMigration, c *model.Certificate) {
	var added []string
	for _, d := range c.Domains {
		for _, n := range m.rw.names(d) {
			if r := m.rw.rename(n); !slices.Contains(c.Domains, r) && !slices.Contains(added, r) {
				added = append(added, r)
			}
		}
	}
	if len(added) == 0 {
		return
	}
	target := fmt.Sprintf("#%d %s", c.Id, strings.Join(c.Domains, ", "))
	if c.Challenge != model.CertChallengeDNS01 {
		m.actions = append(m.actions, DomainMigrateAction{
			Kind: "cert", Target: target, Detail: "+" + strings.Join(added, ", "),
			Error: "not issued here over DNS-01; add the names by hand",
		})
		m.apply = append(m.apply, func() error { return fmt.Errorf("certificate %d is %s", c.Id, c.Challenge) })
		return
	}
	id, domains := c.Id, append(slices.Clone(c.Domains), added...)
	m.add("cert", target, "+"+strings.Join(added, ", ")+", reissued and pushed", func() error {
		row, err := s.certs.GetById(id)
		if err != nil {
			return err
		}
		auto := row.AutoRenew
		if err := s.certs.Update(id, &CertificateRequest{Domains: domains, Challenge: row.Challenge, DnsProviderId: row.DnsProviderId, AutoRenew: &auto}); err != nil {
			return err
		}
		return s.certs.Issue(id)
	})
}

// planEdge copies every shared-port route whose SNI or Host is under the old
// domain, so the new name reaches the same place on each panel.
func (s *DomainMigrateService) planEdge(m *domainMigration, nodes []*model.Node) {
	type panel struct {
		id   int
		name string
		list func() ([]model.EdgeRoute, error)
		save func(*model.EdgeRoute) error
	}
	var edge EdgeService
	panels := []panel{}
	if m.req.coversNode(0) {
		panels = append(panels, panel{0, "this panel", func() ([]model.EdgeRoute, error) {
			rows, err := edge.List()
			out := make([]model.EdgeRoute, 0, len(rows))
			for _, r := range rows {
				out = append(out, *r)
			}
			return out, err
		}, func(r *model.EdgeRoute) error { _, err := edge.Save(r); return err }})
	}
	mgr := runtime.GetManager()
	for _, n := range nodes {
		if n.Role != model.NodeRoleNode || !n.Enable || n.Status != "online" || !m.req.coversNode(n.Id) || mgr == nil {
			continue
		}
		remote, err := mgr.RemoteFor(n)
		if err != nil {
			continue
		}
		panels = append(panels, panel{n.Id, n.Name, func() ([]model.EdgeRoute, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return remote.EdgeRoutes(ctx)
		}, func(r *model.EdgeRoute) error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return remote.SaveEdgeRoute(ctx, r)
		}})
	}
	for _, p := range panels {
		routes, err := p.list()
		if err != nil {
			m.actions = append(m.actions, DomainMigrateAction{Kind: "edge", Target: p.name, Error: err.Error()})
			m.apply = append(m.apply, func() error { return err })
			continue
		}
		for _, r := range routes {
			if len(m.rw.names(r.Match)) == 0 {
				continue
			}
			copyRoute := r
			copyRoute.Id, copyRoute.Match = 0, m.rw.rewrite(strings.ToLower(r.Match))
			exists := slices.ContainsFunc(routes, func(x model.EdgeRoute) bool {
				return x.Kind == copyRoute.Kind && strings.EqualFold(x.Match, copyRoute.Match) && x.PathPrefix == copyRoute.PathPrefix
			})
			if exists {
				continue
			}
			save := p.save
			m.add("edge", p.name, fmt.Sprintf("%s %s%s -> %s %s", r.Kind, copyRoute.Match, r.PathPrefix, r.Action, r.Target), func() error {
				return save(&copyRoute)
			})
		}
	}
}

func (s *DomainMigrateService) planSwitch(m *domainMigration, nodes []*model.Node, hosts []*model.Host, inbounds []*model.Inbound) {
	db := database.GetDB()
	for _, h := range hosts {
		addr, sni, hh := m.rw.rewrite(h.Address), m.rw.rewrite(h.Sni), m.rw.rewrite(h.HostHeader)
		if addr == h.Address && sni == h.Sni && hh == h.HostHeader {
			continue
		}
		id := h.Id
		m.add("host", fmt.Sprintf("%s (#%d)", h.Remark, h.Id), strings.TrimSpace(addr+" "+sni), func() error {
			return db.Model(&model.Host{}).Where("id = ?", id).
				Updates(map[string]any{"address": addr, "sni": sni, "host_header": hh}).Error
		})
	}
	for _, ib := range inbounds {
		owner := 0
		if ib.NodeID != nil {
			owner = *ib.NodeID
		}
		if !m.req.coversNode(owner) {
			continue
		}
		stream, settings := m.rw.rewrite(ib.StreamSettings), m.rw.rewrite(ib.Settings)
		if stream == ib.StreamSettings && settings == ib.Settings {
			continue
		}
		id := ib.Id
		m.add("inbound", fmt.Sprintf("%s (#%d)", ib.Remark, ib.Id), "SNI/Host renamed", func() error {
			var inbounds InboundService
			cur, err := inbounds.GetInbound(id)
			if err != nil {
				return err
			}
			cur.StreamSettings, cur.Settings = m.rw.rewrite(cur.StreamSettings), m.rw.rewrite(cur.Settings)
			_, needRestart, err := inbounds.UpdateInbound(cur)
			if needRestart {
				(&XrayService{}).SetToNeedRestart()
			}
			return err
		})
	}
	for _, n := range nodes {
		addr := m.rw.rewrite(n.Address)
		if addr == n.Address || n.Role != model.NodeRoleNode || !m.req.coversNode(n.Id) {
			continue
		}
		id := n.Id
		m.add("node", n.Name, addr, func() error {
			if err := db.Model(&model.Node{}).Where("id = ?", id).Update("address", addr).Error; err != nil {
				return err
			}
			if mgr := runtime.GetManager(); mgr != nil {
				mgr.InvalidateNode(id)
			}
			return nil
		})
	}
	var settings SettingService
	for _, key := range []string{"subURI", "subJsonURI", "subClashURI", "subDomain", "webDomain"} {
		cur, err := settings.getString(key)
		if err != nil || cur == "" {
			continue
		}
		next := m.rw.rewrite(cur)
		if next == cur {
			continue
		}
		k := key
		m.add("setting", k, next, func() error { return settings.saveSetting(k, next) })
	}
}
