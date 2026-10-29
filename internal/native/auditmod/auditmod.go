// Package auditmod is the audit log as a master module: every change made
// through the API is written to the op log by the master that took it.
package auditmod

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/audit"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Table holds one record per change; its keys come from Host.NextKey.
const Table = "audit"

// KeepDays is how long entries stay.
const KeepDays = 180

// pruneEvery is how often the leader looks for old entries.
var pruneEvery = time.Hour

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string     { return "audit" }
func (*Module) Tables() []string { return []string{Table} }

func (*Module) Legacy() []module.LegacyRoute {
	return []module.LegacyRoute{{Method: "GET", Old: "/panel/api/audit/list", New: "/api/v1/audit"}}
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Middleware writes the entry after the handler ran, so the outcome is known.
// Request bodies are never stored: they carry credentials.
func (*Module) Middleware(h module.Host) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if !audit.Recordable(r.Method, r.URL.Path) {
				return
			}
			name, scope := apiserver.Principal(r)
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			route := r.Pattern
			if _, rest, ok := strings.Cut(route, " "); ok {
				route = rest
			}
			_, _ = h.Set(r.Context(), Table, h.NextKey(), map[string]any{
				"at": h.Now().Unix(), "actor": audit.Actor(name, string(scope)),
				"method": r.Method, "path": route, "status": rec.status, "ip": ip,
			})
		})
	}
}

func (*Module) Mount(r module.Router, h module.Host) {
	r.Handle("GET", "/", module.ScopeMonitor, func(w http.ResponseWriter, req *http.Request) {
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		before, actor := req.URL.Query().Get("before"), req.URL.Query().Get("actor")
		keys := h.Keys(Table)
		out := make([]map[string]any, 0, limit)
		for i := len(keys) - 1; i >= 0 && len(out) < limit; i-- {
			if before != "" && keys[i] >= before {
				continue
			}
			rec, ok := h.Get(Table, keys[i])
			if !ok {
				continue
			}
			entry := map[string]any{"id": keys[i]}
			for k, v := range rec {
				var x any
				_ = json.Unmarshal(v, &x)
				entry[k] = x
			}
			if actor != "" && entry["actor"] != actor {
				continue
			}
			out = append(out, entry)
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
}

// Run lets the leader drop entries older than KeepDays.
func (*Module) Run(ctx context.Context, h module.Host) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if h.Leader(ctx) {
				Prune(ctx, h)
			}
		}
	}
}

// Prune deletes entries older than KeepDays.
func Prune(ctx context.Context, h module.Host) {
	cutoff := h.Now().Add(-KeepDays * 24 * time.Hour).Unix()
	for _, key := range h.Keys(Table) {
		rec, ok := h.Get(Table, key)
		if !ok {
			continue
		}
		var at int64
		_ = json.Unmarshal(rec["at"], &at)
		if at < cutoff {
			_, _ = h.Delete(ctx, Table, key)
		}
	}
}
