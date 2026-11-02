package agent

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/failban"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// BanApplier receives the ban set; *BundleApplier is one.
type BanApplier interface {
	Limits() map[string]int
	SetBans(ctx context.Context, bans []string) error
}

// IPLimit is the node's fail2ban: it reads each user's live addresses and
// bans the ones beyond the user's limit, applying the bans to its own Xray.
type IPLimit struct {
	Xray interface {
		OnlineIPs(context.Context) (map[string][]xrayrun.OnlineIP, error)
	}
	Applier BanApplier
	Limiter *failban.Limiter
	Every   time.Duration // default 10 s
	Log     func(format string, args ...any)
}

func (p *IPLimit) Run(ctx context.Context) {
	t := time.NewTicker(orDefault(p.Every, 10*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *IPLimit) tick(ctx context.Context) {
	observed, err := p.Xray.OnlineIPs(ctx)
	if err != nil {
		return // no Xray yet
	}
	obs := make(map[string]map[string]int64, len(observed))
	for email, ips := range observed {
		m := make(map[string]int64, len(ips))
		for _, e := range ips {
			m[e.IP] = e.LastSeen
		}
		obs[email] = m
	}
	bans := p.Limiter.Observe(obs, p.Applier.Limits())
	if err := p.Applier.SetBans(ctx, bans); err != nil {
		p.logf("ip limit: %v", err)
	}
}

func (p *IPLimit) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}
