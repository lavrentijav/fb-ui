package service

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/edge"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// EdgeService stores the rules of the shared public port and hands a
// validated table to the running web server.
type EdgeService struct {
	settingService SettingService
}

// EdgeSettings is the port-wide part: where the edge listens and what
// happens to a connection no route claims.
type EdgeSettings struct {
	Enable               bool   `json:"enable" form:"enable" example:"false"`
	Listen               string `json:"listen" form:"listen" example:":443"`
	DefaultAction        string `json:"defaultAction" form:"defaultAction" example:"tcp"`
	DefaultTarget        string `json:"defaultTarget" form:"defaultTarget" example:"127.0.0.1:8443"`
	DefaultProxyProtocol int    `json:"defaultProxyProtocol" form:"defaultProxyProtocol" example:"1"`
}

// EdgeRuntime is everything the web server needs to (re)configure the edge.
type EdgeRuntime struct {
	Settings EdgeSettings
	Config   edge.Config
	HTTP     []edge.HTTPRoute
}

// EdgeStatus reports what is actually running, not what is stored.
type EdgeStatus struct {
	Running   bool   `json:"running" example:"true"`
	Listen    string `json:"listen" example:"[::]:443"`
	LastError string `json:"lastError" example:""`
	PanelTLS  bool   `json:"panelTls" example:"true"`
}

// EdgeTargetCheck is whether a route's local target answers right now.
type EdgeTargetCheck struct {
	RouteId int    `json:"routeId" example:"1"`
	Target  string `json:"target" example:"127.0.0.1:8443"`
	Ok      bool   `json:"ok" example:"true"`
	Error   string `json:"error" example:""`
}

var (
	edgeMu       sync.Mutex
	edgeApplier  func(EdgeRuntime) error
	edgeStatusFn func() EdgeStatus
)

// SetEdgeRuntime is called by the web server that owns the listeners.
func SetEdgeRuntime(apply func(EdgeRuntime) error, status func() EdgeStatus) {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	edgeApplier, edgeStatusFn = apply, status
}

func (s *EdgeService) Status() EdgeStatus {
	edgeMu.Lock()
	fn := edgeStatusFn
	edgeMu.Unlock()
	if fn == nil {
		return EdgeStatus{}
	}
	return fn()
}

func (s *EdgeService) GetSettings() (*EdgeSettings, error) {
	var out EdgeSettings
	var err error
	if out.Enable, err = s.settingService.getBool("edgeEnable"); err != nil {
		return nil, err
	}
	if out.Listen, err = s.settingService.getString("edgeListen"); err != nil {
		return nil, err
	}
	if out.DefaultAction, err = s.settingService.getString("edgeDefaultAction"); err != nil {
		return nil, err
	}
	if out.DefaultTarget, err = s.settingService.getString("edgeDefaultTarget"); err != nil {
		return nil, err
	}
	if out.DefaultProxyProtocol, err = s.settingService.getInt("edgeDefaultProxyProtocol"); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *EdgeService) saveSettings(v *EdgeSettings) error {
	for k, val := range map[string]string{
		"edgeEnable":               strconv.FormatBool(v.Enable),
		"edgeListen":               v.Listen,
		"edgeDefaultAction":        v.DefaultAction,
		"edgeDefaultTarget":        v.DefaultTarget,
		"edgeDefaultProxyProtocol": strconv.Itoa(v.DefaultProxyProtocol),
	} {
		if err := s.settingService.setString(k, val); err != nil {
			return err
		}
	}
	return nil
}

// SetSettings stores and applies; if the edge cannot start on the new
// settings (a busy port), the previous ones are restored and applied again.
func (s *EdgeService) SetSettings(v *EdgeSettings) error {
	v.Listen = strings.TrimSpace(v.Listen)
	if v.Listen == "" {
		v.Listen = ":443"
	}
	if _, port, err := net.SplitHostPort(v.Listen); err != nil || port == "" {
		return common.NewErrorf("listen address %q is not [host]:port", v.Listen)
	}
	routes, err := s.List()
	if err != nil {
		return err
	}
	if _, err := buildEdgeRuntime(v, routes); err != nil {
		return err
	}
	prev, err := s.GetSettings()
	if err != nil {
		return err
	}
	if err := s.saveSettings(v); err != nil {
		return err
	}
	if err := s.Apply(); err != nil {
		_ = s.saveSettings(prev)
		_ = s.Apply()
		return common.NewErrorf("edge not changed: %v", err)
	}
	return nil
}

func (s *EdgeService) List() ([]*model.EdgeRoute, error) {
	var rows []*model.EdgeRoute
	err := database.GetDB().Order("sort_order asc, id asc").Find(&rows).Error
	return rows, err
}

func normalizeEdgeRoute(r *model.EdgeRoute) {
	r.Name = strings.TrimSpace(r.Name)
	r.Kind = strings.TrimSpace(r.Kind)
	r.Match = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Match), "."))
	r.PathPrefix = strings.TrimSpace(r.PathPrefix)
	r.Action = strings.TrimSpace(r.Action)
	r.Target = strings.TrimSpace(r.Target)
	if r.Kind == "" {
		r.Kind = model.EdgeKindSNI
	}
	if r.Kind == model.EdgeKindHTTP {
		r.Action = model.EdgeActionProxy
		if r.PathPrefix == "" {
			r.PathPrefix = "/"
		}
	}
}

// Save creates (Id 0) or replaces a route after checking the whole table it
// would produce, so a route that cannot work is never stored.
func (s *EdgeService) Save(r *model.EdgeRoute) (*model.EdgeRoute, error) {
	normalizeEdgeRoute(r)
	routes, err := s.List()
	if err != nil {
		return nil, err
	}
	next := make([]*model.EdgeRoute, 0, len(routes)+1)
	found := false
	for _, x := range routes {
		if x.Id == r.Id && r.Id != 0 {
			next = append(next, r)
			found = true
			continue
		}
		next = append(next, x)
	}
	if r.Id != 0 && !found {
		return nil, common.NewErrorf("edge route %d not found", r.Id)
	}
	if r.Id == 0 {
		next = append(next, r)
	}
	settings, err := s.GetSettings()
	if err != nil {
		return nil, err
	}
	if _, err := buildEdgeRuntime(settings, next); err != nil {
		return nil, err
	}
	if err := database.GetDB().Save(r).Error; err != nil {
		return nil, err
	}
	return r, s.Apply()
}

func (s *EdgeService) Delete(id int) error {
	if err := database.GetDB().Where("id = ?", id).Delete(&model.EdgeRoute{}).Error; err != nil {
		return err
	}
	return s.Apply()
}

// Apply hands the stored table to the running server.
func (s *EdgeService) Apply() error {
	settings, err := s.GetSettings()
	if err != nil {
		return err
	}
	routes, err := s.List()
	if err != nil {
		return err
	}
	rt, err := buildEdgeRuntime(settings, routes)
	if err != nil {
		return err
	}
	edgeMu.Lock()
	apply := edgeApplier
	edgeMu.Unlock()
	if apply == nil {
		return nil
	}
	return apply(*rt)
}

func edgeRoute(action, target string, proxyProtocol int) edge.Route {
	switch action {
	case model.EdgeActionPanel:
		return edge.Route{Action: edge.ActionHandoff, Target: "panel"}
	case model.EdgeActionTCP:
		return edge.Route{Action: edge.ActionTCP, Target: target, ProxyProtocol: proxyProtocol}
	default:
		return edge.Route{Action: edge.ActionReject}
	}
}

var errEdgeHidesPanel = errors.New("an http route for every host on / would hide the panel itself; give it a host or a narrower path")

func buildEdgeRuntime(settings *EdgeSettings, routes []*model.EdgeRoute) (*EdgeRuntime, error) {
	out := &EdgeRuntime{Settings: *settings}
	switch settings.DefaultAction {
	case model.EdgeActionPanel, model.EdgeActionTCP, model.EdgeActionReject:
	default:
		return nil, common.NewErrorf("default action %q must be panel, tcp or reject", settings.DefaultAction)
	}
	out.Config.Default = edgeRoute(settings.DefaultAction, settings.DefaultTarget, settings.DefaultProxyProtocol)
	for _, r := range routes {
		if !r.Enable {
			continue
		}
		label := r.Name
		if label == "" {
			label = r.Match
		}
		switch r.Kind {
		case model.EdgeKindSNI:
			switch r.Action {
			case model.EdgeActionPanel, model.EdgeActionTCP, model.EdgeActionReject:
			default:
				return nil, common.NewErrorf("route %q: an sni route takes panel, tcp or reject, not %q", label, r.Action)
			}
			out.Config.Routes = append(out.Config.Routes, func() edge.Route {
				er := edgeRoute(r.Action, r.Target, r.ProxyProtocol)
				er.Match = r.Match
				return er
			}())
		case model.EdgeKindHTTP:
			if r.Match == "" && r.PathPrefix == "/" {
				return nil, common.NewErrorf("route %q: %v", label, errEdgeHidesPanel)
			}
			u, err := url.Parse(r.Target)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, common.NewErrorf("route %q: target %q must be an http(s) URL", label, r.Target)
			}
			out.HTTP = append(out.HTTP, edge.HTTPRoute{Host: r.Match, PathPrefix: r.PathPrefix, Target: r.Target, StripPrefix: r.StripPrefix})
		default:
			return nil, common.NewErrorf("route %q: kind %q must be sni or http", label, r.Kind)
		}
	}
	if err := out.Config.Validate([]string{"panel"}); err != nil {
		return nil, common.NewError(err.Error())
	}
	if err := edge.NewHTTPRouter(nil, nil).Update(out.HTTP); err != nil {
		return nil, common.NewError(err.Error())
	}
	return out, nil
}

// CheckTargets dials every enabled tcp target and http upstream, so an
// operator sees a dead backend before moving traffic onto the edge.
func (s *EdgeService) CheckTargets() ([]EdgeTargetCheck, error) {
	routes, err := s.List()
	if err != nil {
		return nil, err
	}
	settings, err := s.GetSettings()
	if err != nil {
		return nil, err
	}
	type probe struct {
		id   int
		addr string
	}
	var probes []probe
	if settings.DefaultAction == model.EdgeActionTCP {
		probes = append(probes, probe{0, settings.DefaultTarget})
	}
	for _, r := range routes {
		if !r.Enable {
			continue
		}
		switch {
		case r.Kind == model.EdgeKindSNI && r.Action == model.EdgeActionTCP:
			probes = append(probes, probe{r.Id, r.Target})
		case r.Kind == model.EdgeKindHTTP:
			if u, err := url.Parse(r.Target); err == nil {
				host := u.Host
				if u.Port() == "" {
					port := "80"
					if u.Scheme == "https" {
						port = "443"
					}
					host = net.JoinHostPort(u.Hostname(), port)
				}
				probes = append(probes, probe{r.Id, host})
			}
		}
	}
	out := make([]EdgeTargetCheck, 0, len(probes))
	for _, p := range probes {
		c := EdgeTargetCheck{RouteId: p.id, Target: p.addr}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.addr)
		cancel()
		if err != nil {
			c.Error = err.Error()
		} else {
			c.Ok = true
			_ = conn.Close()
		}
		out = append(out, c)
	}
	return out, nil
}
