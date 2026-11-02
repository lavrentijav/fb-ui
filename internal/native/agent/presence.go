package agent

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

// Presence publishes this node's online users and re-routes the network
// whenever the fleet's presence changes.
type Presence struct {
	Node  string
	Xray  interface{ Online(context.Context) ([]string, error) }
	Map   *presence.Map
	Conn  func(ctx context.Context) (*nats.Conn, error)
	Every time.Duration // default 2 s
	Log   func(format string, args ...any)

	repatch func(context.Context) error
	watched *nats.Conn
}

func NewPresence(node string, x interface{ Online(context.Context) ([]string, error) }, m *presence.Map,
	a *BundleApplier, conn func(ctx context.Context) (*nats.Conn, error), log func(string, ...any)) *Presence {
	return &Presence{Node: node, Xray: x, Map: m, Conn: conn, Log: log, repatch: a.Repatch}
}

func (p *Presence) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func (p *Presence) Run(ctx context.Context) {
	go p.watch(ctx)
	t := time.NewTicker(orDefault(p.Every, 2*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.publish(ctx)
		}
	}
}

func (p *Presence) publish(ctx context.Context) {
	users, err := p.Xray.Online(ctx)
	if err != nil {
		return // no Xray yet
	}
	nc, err := p.Conn(ctx)
	if err != nil {
		return
	}
	if nc != p.watched { // a new master: subscribe there
		if _, err := p.Map.Watch(nc); err != nil {
			p.logf("presence: %v", err)
			return
		}
		p.watched = nc
	}
	if err := presence.Publish(nc, p.Node, users, time.Now()); err != nil {
		p.logf("presence: %v", err)
	}
}

func (p *Presence) watch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.Map.Changed():
			if err := p.repatch(ctx); err != nil {
				p.logf("network routes: %v", err)
			}
		}
	}
}
