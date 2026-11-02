package agent

import (
	"context"
	"sort"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// QuotaSource answers a client's locally counted traffic, in bytes.
type QuotaSource interface {
	Used(ctx context.Context, email string) (total int64, known bool, err error)
}

// QuotaTarget is the config a node stops a client in, plus that client's
// allowance from the last applied bundle.
type QuotaTarget interface {
	Quotas() map[string]nodespec.Quota
	SetDisabled(ctx context.Context, emails []string) error
}

// Quota stops a client locally once its allowance is spent or it expired.
//
// The master stays authoritative: it renders the client out of the node's spec
// when its own reconciler sees the same thing, and its correction then wins.
// This loop exists for the gap -- a master unreachable, or reports a little
// behind -- where the user would otherwise keep getting free traffic.
type Quota struct {
	Source  QuotaSource
	Target  QuotaTarget
	Every   time.Duration // default 30 s
	Now     func() time.Time
	Log     func(format string, args ...any)
	last    []string
	lastSet bool
}

func (q *Quota) Run(ctx context.Context) {
	t := time.NewTicker(orDefault(q.Every, 30*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q.Tick(ctx)
		}
	}
}

// Tick recomputes whom to stop and applies the difference. It answers the set
// it decided on, so a test can drive it without a clock.
func (q *Quota) Tick(ctx context.Context) []string {
	quotas := q.Target.Quotas()
	now := time.Now()
	if q.Now != nil {
		now = q.Now()
	}
	var stop []string
	for email, quota := range quotas {
		if email == "" {
			continue
		}
		if quota.ExpiresAt > 0 && now.UnixMilli() >= quota.ExpiresAt {
			stop = append(stop, email)
			continue
		}
		if quota.TotalBytes <= 0 {
			continue // unlimited
		}
		used, known, err := q.Source.Used(ctx, email)
		if err != nil || !known {
			continue
		}
		if used >= quota.TotalBytes {
			stop = append(stop, email)
		}
	}
	sort.Strings(stop)
	if q.lastSet && equalStrings(q.last, stop) {
		return stop
	}
	q.last, q.lastSet = stop, true
	if err := q.Target.SetDisabled(ctx, stop); err != nil {
		q.logf("quota: %v", err)
		return stop
	}
	for _, email := range stop {
		q.logf("quota: stopped %s locally (allowance spent or expired)", email)
	}
	return stop
}

func (q *Quota) logf(format string, args ...any) {
	if q.Log != nil {
		q.Log(format, args...)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
