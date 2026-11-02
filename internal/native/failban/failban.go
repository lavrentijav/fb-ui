// Package failban turns per-user IP observations into a set of banned source
// addresses: a user keeps its LimitIP newest addresses, the older ones are
// blocked while they stay around.
package failban

import (
	"slices"
	"sync"
	"time"
)

// HoldDefault is how long an address counts as live after it was last seen.
// It keeps a just-banned device from reconnecting as the newest address
// while the other one is pushed out.
const HoldDefault = 2 * time.Minute

type Limiter struct {
	// Hold is how long an address counts as live after it was last seen;
	// default HoldDefault.
	Hold time.Duration
	Now  func() time.Time

	mu   sync.Mutex
	seen map[string]map[string]entry // email -> ip
}

// entry keeps both ends of an address' life: the order in which addresses
// appeared decides which ones are over the limit, since the core may report
// 0 or a stale lastSeen.
type entry struct{ first, last int64 }

func (l *Limiter) hold() time.Duration {
	if l.Hold > 0 {
		return l.Hold
	}
	return HoldDefault
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

type seen struct {
	ip string
	at int64
}

// Observe folds one scan (email -> ip -> last seen, unix seconds) into the
// state and returns the addresses over their user's limit: the newest LimitIP
// addresses of a user stay, the older ones are banned.
func (l *Limiter) Observe(observed map[string]map[string]int64, limits map[string]int) []string {
	now, hold := l.now().Unix(), int64(l.hold().Seconds())
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]map[string]entry{}
	}
	for email, ips := range observed {
		m := l.seen[email]
		if m == nil {
			m = map[string]entry{}
			l.seen[email] = m
		}
		for ip := range ips {
			e, ok := m[ip]
			if !ok {
				e.first = now // the API's lastSeen is 0 or stale on some cores
			}
			e.last = now
			m[ip] = e
		}
	}
	var banned []string
	for email, m := range l.seen {
		for ip, e := range m {
			if now-e.last > hold {
				delete(m, ip)
			}
		}
		if len(m) == 0 {
			delete(l.seen, email)
			continue
		}
		limit := limits[email]
		if limit <= 0 || len(m) <= limit {
			continue
		}
		all := make([]seen, 0, len(m))
		for ip, e := range m {
			all = append(all, seen{ip, e.first})
		}
		slices.SortFunc(all, func(a, b seen) int {
			if a.at != b.at {
				if a.at < b.at {
					return -1
				}
				return 1
			}
			switch {
			case a.ip < b.ip:
				return -1
			case a.ip > b.ip:
				return 1
			}
			return 0
		})
		// oldest first: the newest limit entries stay, the rest are banned
		for _, s := range all[:len(all)-limit] {
			banned = append(banned, s.ip)
		}
	}
	slices.Sort(banned)
	return slices.Compact(banned)
}
