// Package module is the contract between a master and its optional parts. A
// module sees the master only through Host, so it can be tested without a
// bus and cut out of a build with a tag.
package module

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Scope is what an API token may do.
type Scope string

const (
	// ScopePublic marks a route that needs no token (a login page).
	ScopePublic  Scope = ""
	ScopeAdmin   Scope = "admin"
	ScopeMonitor Scope = "monitor"
)

// EventTable holds the events modules announce; delivery reads it.
const EventTable = "event"

// Event is something worth telling the outside world about.
type Event struct {
	Type   string          `json:"type"`
	Source string          `json:"source,omitempty"`
	At     int64           `json:"at"` // unix ms
	Data   json.RawMessage `json:"data,omitempty"`
}

// Host is the master as a module sees it.
type Host interface {
	// Name is this master's name; it is the origin of what it writes.
	Name() string
	Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error)
	Delete(ctx context.Context, table, key string) (oplog.Op, error)
	Get(table, key string) (map[string]json.RawMessage, bool)
	// Keys lists the live keys of a table, sorted.
	Keys(table string) []string
	// Leader reports whether this master holds the fleet-wide "leader" lease:
	// the one that runs jobs that must happen once, not once per master.
	Leader(ctx context.Context) bool
	// NextKey is a record key no other write on any master will use and that
	// sorts by creation time on one master: "<master>:<16 hex digits>".
	NextKey() string
	// Emit appends an event to EventTable under NextKey.
	Emit(ctx context.Context, e Event) error
	Now() time.Time
}

// Router registers a module's routes under /api/v1/<module>.
type Router interface {
	Handle(method, pattern string, scope Scope, h http.HandlerFunc)
}

// LegacyRoute maps an old panel route onto a module route. Old and New are
// full paths; a {name} segment in Old is carried into New. The old panel used
// POST for changes, so NewMethod names the method of the new route when it
// differs from Method. Wrap, when set, turns the new response body into the
// old "obj".
type LegacyRoute struct {
	Method, Old, New string
	NewMethod        string
	Wrap             func(newBody []byte) ([]byte, error)
	// Rewrite may change the method, path and body before the call, for an old
	// route that carries its parameters where the new one cannot read them (a
	// delete by name in the body). It receives the resolved new path and the
	// request body; returning newBody == nil keeps the original body.
	Rewrite func(newMethod, newPath string, body []byte) (method, path string, newBody []byte, err error)
}

// Module is one optional part of a master.
type Module interface {
	Name() string
	Tables() []string
	Mount(r Router, h Host)
	Legacy() []LegacyRoute
	// Run is the module's background work; it returns when ctx ends. It runs on
	// every master, so work that must happen once checks Host.Leader.
	Run(ctx context.Context, h Host)
}

// ReportSource is the node-report streams of every master (the bus).
type ReportSource interface {
	ReportRange(ctx context.Context, master string) (first, last uint64, err error)
	GetReport(ctx context.Context, master string, seq uint64) ([]byte, error)
}

// ReportAware is implemented by a module that needs the report streams, which
// only a running master can give it.
type ReportAware interface {
	SetReportSource(src ReportSource, masters []string)
}

// SessionAuth is implemented by a module that turns a browser cookie into a
// principal, so the interface does not keep an API token in JavaScript.
type SessionAuth interface {
	Principal(cookie string) (name string, scope Scope, ok bool)
}

// Middlewarer is implemented by a module that wraps every API request
// (audit does).
type Middlewarer interface {
	Middleware(h Host) func(http.Handler) http.Handler
}

// Writer writes records; Host implements it.
type Writer interface {
	Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error)
}

// Reader is the read side of the projected state.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	// Keys lists the live keys of a table, sorted.
	Keys(table string) []string
}

// SpecContributor is implemented by a module that adds to a node's spec when a
// master renders it (routing adds its rules). Anything already in the spec was
// put there on purpose for that node, so a contributor appends after it.
type SpecContributor interface {
	ContributeSpec(r Reader, node string, spec *nodespec.Spec)
}
