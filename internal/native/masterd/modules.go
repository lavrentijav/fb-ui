package masterd

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/mhost"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// startModules hands the master to every registered module and, when asked
// to, serves their API.
func (d *Daemon) startModules(ctx context.Context, m *master.Master) {
	h := mhost.New(m, d.store.State, mhost.Options{})
	d.host.Store(h)
	for _, mod := range module.All() {
		if ra, ok := mod.(module.ReportAware); ok {
			ra.SetReportSource(d.bus, d.cfg.Masters)
		}
		go mod.Run(ctx, h)
	}
	if d.cfg.APIListen == "" {
		return
	}
	go d.bootstrapToken(ctx, h)
	d.api = &http.Server{Addr: d.cfg.APIListen, Handler: d.APIHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.api.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.logf("api: %v", err)
		}
	}()
}

// APIHandler is /api/v1 over the registered modules; nil until the master is
// ready. The handler is built once: building it mounts every module.
func (d *Daemon) APIHandler() http.Handler {
	if h, ok := d.apiHandler.Load().(http.Handler); ok {
		return h
	}
	host := d.host.Load()
	if host == nil {
		return nil
	}
	h := withLegacy(module.All(), apiserver.New(host, module.All()))
	d.apiHandler.Store(h)
	return h
}

// bootstrapToken gives a master with no tokens at all its first admin token.
// It waits for the op log to catch up first, so a master joining a fleet that
// already has tokens does not mint another, and it logs the token once.
func (d *Daemon) bootstrapToken(ctx context.Context, h *mhost.Host) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	if len(h.Keys(apiauth.Table)) > 0 || !h.Leader(ctx) {
		return
	}
	token, err := apiauth.Create(ctx, h, "bootstrap", module.ScopeAdmin, 0)
	if err != nil {
		d.logf("api: bootstrap token: %v", err)
		return
	}
	d.logf("api: first admin token (shown once): %s", token)
}

// contribute lets every module that wants to add to a node's spec do so.
func contribute(mods []module.Module, r module.Reader, node string, spec *nodespec.Spec) {
	for _, m := range mods {
		if c, ok := m.(module.SpecContributor); ok {
			c.ContributeSpec(r, node, spec)
		}
	}
}
