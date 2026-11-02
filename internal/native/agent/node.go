package agent

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/edge"

	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// Xray is what the node loop needs from the proxy core.
type Xray interface {
	Apply(ctx context.Context, config []byte) error
	Restart() error
	Running() bool
	Traffic(ctx context.Context) ([]xrayrun.UserTraffic, error)
}

// Node is the whole agent loop: collect traffic into the journal, send it,
// pull desired state from the bound master, keep Xray running.
type Node struct {
	Name    string
	Sender  *Sender
	Applier *desired.Applier
	Xray    Xray
	Log     func(format string, args ...any)
	// DrainState reports maintenance from the edge; nil means never draining.
	DrainState func() edge.DrainState

	ReportEvery time.Duration // 0 = 10s
	PullEvery   time.Duration // 0 = 15s
	// Heartbeat sends an empty report when nothing moved, so masters can tell
	// a quiet node from a dead one (0 = 30s).
	Heartbeat time.Duration
	// Health, when set, is read on each report (load, memory, uptime).
	Health func() reports.Health
	// Version is the agent's build version, reported with health.
	Version string

	lastReport time.Time
}

// Start brings the last applied state up before any master answers.
func (n *Node) Start(ctx context.Context) error { return n.Applier.Reapply(ctx) }

// Collect journals the traffic Xray counted since the last call.
func (n *Node) Collect(ctx context.Context) {
	if n.Applier.Current().Hash == "" {
		return // no config yet, so no Xray to ask
	}
	traffic, err := n.Xray.Traffic(ctx)
	if err != nil {
		n.logf("traffic: %v", err)
		return
	}
	r := reports.Report{At: time.Now().UnixMilli()}
	if n.Health != nil {
		h := n.Health()
		if n.Version != "" {
			h.Version = n.Version
		}
		r.Health = &h
	}
	if n.DrainState != nil {
		// while draining, report even without traffic: the master watches Active fall
		if st := n.DrainState(); st.Enable {
			r.Drain = &reports.Drain{Since: st.Since, Active: st.Active}
		}
	}
	if n.lastReport.IsZero() {
		n.lastReport = time.Now() // a fresh start is not yet a missed heartbeat
	}
	// a heartbeat carries health even with no traffic; a busy node sends its
	// vitals with the traffic it already has
	if len(traffic) == 0 && r.Drain == nil && time.Since(n.lastReport) < n.heartbeat() {
		return
	}
	for _, t := range traffic {
		r.Traffic = append(r.Traffic, reports.Traffic{Email: t.Email, Up: t.Up, Down: t.Down})
		r.Online = append(r.Online, t.Email)
	}
	if _, err := n.Sender.Journal.Add(r); err != nil {
		n.logf("journal: %v", err)
		return
	}
	n.lastReport = time.Now()
}

func (n *Node) heartbeat() time.Duration {
	if n.Heartbeat > 0 {
		return n.Heartbeat
	}
	return 30 * time.Second
}

// Pull asks the bound master for a newer state and applies it.
func (n *Node) Pull(ctx context.Context) {
	master := n.Sender.Select.Current()
	if master == "" {
		var err error
		if master, err = n.Sender.Select.Tick(ctx); master == "" {
			n.logf("no master: %v", err)
			return
		}
	}
	nc, err := n.Sender.Conn(ctx, master)
	if err != nil {
		n.Sender.Select.Failed(master)
		n.logf("connect %s: %v", master, err)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := n.Applier.Pull(pctx, nc, master, n.Name); err != nil {
		n.logf("desired from %s: %v", master, err)
	}
}

// Run loops until ctx ends.
func (n *Node) Run(ctx context.Context) {
	report, pull := orDefault(n.ReportEvery, 10*time.Second), orDefault(n.PullEvery, 15*time.Second)
	rt, pt := time.NewTicker(report), time.NewTicker(pull)
	defer rt.Stop()
	defer pt.Stop()
	defer n.Sender.Close()
	n.Pull(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.C:
			n.Collect(ctx)
			if _, err := n.Sender.Flush(ctx); err != nil {
				n.logf("flush: %v", err)
			}
		case <-pt.C:
			n.Pull(ctx)
			if n.Applier.Current().Hash != "" && !n.Xray.Running() {
				n.logf("xray is down, restarting")
				if err := n.Xray.Restart(); err != nil {
					n.logf("restart: %v", err)
				}
			}
		}
	}
}

func (n *Node) logf(format string, args ...any) {
	if n.Log != nil {
		n.Log(format, args...)
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
