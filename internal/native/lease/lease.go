// Package lease lets one master of many do a job: a claim written through the
// op log, left to replicate, and kept only if no other master's claim won.
package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

// Reader is the projected state.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
}

type Lease struct {
	Master *master.Master
	State  Reader
	Table  string
	TTL    time.Duration // how long a claim holds
	Settle time.Duration // how long a claim replicates before it is checked
	Now    func() time.Time
}

func (l Lease) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Holder is who holds key's live claim, "" if nobody.
func (l Lease) Holder(key string) string {
	rec, ok := l.State.Get(l.Table, key)
	if !ok {
		return ""
	}
	var holder string
	var until int64
	_ = json.Unmarshal(rec["holder"], &holder)
	_ = json.Unmarshal(rec["until"], &until)
	if time.UnixMilli(until).Before(l.now()) {
		return ""
	}
	return holder
}

// Claim takes or renews key unless another master holds it; of two masters
// claiming at once, the op log's last writer holds it on both.
func (l Lease) Claim(ctx context.Context, key string) (bool, error) {
	self := l.Master.Name()
	if h := l.Holder(key); h != "" && h != self {
		return false, nil
	}
	until := l.now().Add(l.TTL).UnixMilli()
	if _, err := l.Master.Set(ctx, l.Table, key, map[string]any{"holder": self, "until": until}); err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(l.Settle):
	}
	if _, err := l.Master.CatchUp(ctx); err != nil {
		return false, err
	}
	return l.Holder(key) == self, nil
}
