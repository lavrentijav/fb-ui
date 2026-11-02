// Package pool keeps a name that resolves to every live master (the panel's
// and the subscriptions' address): each master checks each master the way a
// client would, and one of them, by lease, writes the records.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lease"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

const (
	// MastersTable holds each master's public addresses, written by the master
	// itself ("ips", plus the first as the legacy "ip").
	MastersTable = "masters"
	// HealthTable holds one master's verdict on one address, keyed
	// "<master>/<ip>", so every master decides on the whole fleet's answers.
	HealthTable = "iphealth"
	// Table holds what was last written to DNS per name.
	Table      = "dnspool"
	LeaseTable = "poollease"
)

type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

type Pool struct {
	Master *master.Master
	State  Reader
	Name   string // master.firewalk.top
	// Check tells whether a master answers on ip as a client would reach it.
	Check func(ctx context.Context, ip string) error
	DNS   func(name string) (dnsprovider.Provider, string, error)
	// Down is how many failed checks in a row a master calls an address down
	// (default 3). The fleet decides by majority, not one master's route.
	Down int
	// MaxDownFraction is the share of reporting masters that must call an
	// address down before it leaves DNS (default 0.5); 1 disables the
	// majority and lets one master drop it.
	MaxDownFraction float64
	// MaxRecords caps how many addresses a name resolves to (0 = every live
	// one); when one blocks, the next live address fills its place.
	MaxRecords int
	// HealthTTL is how long a master's verdict counts (default 5 min).
	HealthTTL time.Duration
	TTL       int           // DNS record TTL in seconds (default 60)
	Lease     time.Duration // the writer's lease (default 2 min)
	Settle    time.Duration // default 5 s
	Logf      func(format string, args ...any)

	mu     sync.Mutex
	failed map[string]int // ip -> failed checks in a row
}

// clean normalizes an address list: trimmed, deduped, sorted.
func clean(ips []string) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip = strings.TrimSpace(ip); ip != "" {
			out = append(out, ip)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func known(state Reader, name string) (ips []string, manual bool, ok bool) {
	rec, ok := state.Get(MastersTable, name)
	if !ok {
		return nil, false, false
	}
	_ = json.Unmarshal(rec["ips"], &ips)
	if len(ips) == 0 {
		// an older record carried one address
		var one string
		if json.Unmarshal(rec["ip"], &one) == nil && one != "" {
			ips = []string{one}
		}
	}
	_ = json.Unmarshal(rec["manual"], &manual)
	return clean(ips), manual, true
}

// Announce records this master's public addresses for the others. It leaves
// an operator's manual list alone, so the panel's own detection cannot
// overwrite what was set by hand.
func Announce(ctx context.Context, m *master.Master, state Reader, ips []string) error {
	return announce(ctx, m, state, ips, false)
}

// SetManual records an operator's addresses; auto-detection then leaves them.
func SetManual(ctx context.Context, m *master.Master, state Reader, ips []string) error {
	return announce(ctx, m, state, ips, true)
}

// AnnounceAuto records addresses the master detected itself, unless an
// operator set a manual list.
func AnnounceAuto(ctx context.Context, m *master.Master, state Reader, ips []string) error {
	if _, manual, ok := known(state, m.Name()); ok && manual {
		return nil
	}
	return announce(ctx, m, state, ips, false)
}

func announce(ctx context.Context, m *master.Master, state Reader, ips []string, manual bool) error {
	ips = clean(ips)
	if cur, curManual, ok := known(state, m.Name()); ok && curManual == manual && slices.Equal(cur, ips) {
		return nil
	}
	fields := map[string]any{"ips": ips, "manual": manual}
	if len(ips) > 0 {
		fields["ip"] = ips[0] // for an older master reading this record
	}
	_, err := m.Set(ctx, MastersTable, m.Name(), fields)
	return err
}

// Manual answers the operator's address list when one is set.
func Manual(state Reader, name string) ([]string, bool) {
	ips, manual, ok := known(state, name)
	if !ok || !manual {
		return nil, false
	}
	return ips, true
}

// DetectIPs lists the global unicast addresses of this host's interfaces, so
// a master whose address changed (a new public IP, a moved machine) announces
// the new one without anyone typing it in.
func DetectIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		out = append(out, ip.String())
	}
	return clean(out)
}

func (p *Pool) members() []string {
	var ips []string
	for _, name := range p.State.Keys(MastersTable) {
		got, _, ok := known(p.State, name)
		if ok {
			ips = append(ips, got...)
		}
	}
	return clean(ips)
}

// health is the fleet's verdict on one address: how many masters report it
// and how many of those call it down.
type health struct {
	ip    string
	fail  int
	down  int // masters calling it down
	count int // masters reporting it
}

// publish writes this master's verdict on every address it checked, but only
// when the verdict changed or the record is old enough to need refreshing.
func (p *Pool) publish(ctx context.Context, verdicts map[string]int, down int) {
	if p.Master == nil {
		return
	}
	ttl := p.HealthTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now()
	for ip, failures := range verdicts {
		key := p.Master.Name() + "/" + ip
		isDown := failures >= down
		if rec, ok := p.State.Get(HealthTable, key); ok {
			var haveDown bool
			var at int64
			_ = json.Unmarshal(rec["down"], &haveDown)
			_ = json.Unmarshal(rec["at"], &at)
			if haveDown == isDown && now.UnixMilli()-at < int64(ttl/2/time.Millisecond) {
				continue
			}
		}
		_, _ = p.Master.Set(ctx, HealthTable, key, map[string]any{
			"ip": ip, "master": p.Master.Name(), "fail": failures, "down": isDown, "at": now.UnixMilli(),
		})
	}
}

// fleetHealth folds the published verdicts into one answer per address, and
// ignores masters that stopped reporting within HealthTTL.
func (p *Pool) fleetHealth() map[string]health {
	ttl := p.HealthTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	cutoff := time.Now().Add(-ttl).UnixMilli()
	out := map[string]health{}
	own := ""
	if p.Master != nil {
		own = p.Master.Name() + "/"
	}
	for _, key := range p.State.Keys(HealthTable) {
		if own != "" && strings.HasPrefix(key, own) {
			continue // folded in from this master's fresh verdict, not a stale record
		}
		rec, ok := p.State.Get(HealthTable, key)
		if !ok {
			continue
		}
		var ip string
		var fail int
		var at int64
		var down bool
		_ = json.Unmarshal(rec["ip"], &ip)
		_ = json.Unmarshal(rec["fail"], &fail)
		_ = json.Unmarshal(rec["at"], &at)
		_ = json.Unmarshal(rec["down"], &down)
		if ip == "" || at < cutoff {
			continue
		}
		cur := out[ip]
		cur.ip = ip
		cur.count++
		if down {
			cur.down++
		}
		if fail > cur.fail {
			cur.fail = fail
		}
		out[ip] = cur
	}
	return out
}

// live checks every address and returns those the fleet still reaches: an
// address leaves when enough masters call it down, so one master's blocked
// route does not move everybody off a working address.
func (p *Pool) live(ctx context.Context) []string {
	ips := p.members()
	results := make([]error, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			results[i] = p.Check(cctx, ip)
		})
	}
	wg.Wait()
	down := p.Down
	if down <= 0 {
		down = 3
	}
	p.mu.Lock()
	if p.failed == nil {
		p.failed = map[string]int{}
	}
	verdicts := make(map[string]int, len(ips))
	for i, ip := range ips {
		if results[i] == nil {
			p.failed[ip] = 0
		} else {
			p.failed[ip]++
		}
		verdicts[ip] = p.failed[ip]
	}
	p.mu.Unlock()

	p.publish(ctx, verdicts, down)

	fleet := p.fleetHealth()
	fraction := p.MaxDownFraction
	if fraction <= 0 {
		fraction = 0.5
	}
	var live []string
	for _, ip := range ips {
		h := fleet[ip]
		count, downs := h.count, h.down
		if p.Master != nil {
			// this master's verdict is current, without waiting for the write
			// to replicate back
			count++
			if verdicts[ip] >= down {
				downs++
			}
		}
		if count > 0 && float64(downs)/float64(count) >= fraction {
			continue // enough masters cannot reach it: use the next address
		}
		live = append(live, ip)
	}
	// If more than half the known addresses look gone, the fleet's own view is
	// more likely broken than the addresses: keep the last good records rather
	// than shrinking DNS to whatever one master still reaches.
	if len(ips) > 1 && len(live)*2 < len(ips) {
		return nil
	}
	if p.MaxRecords > 0 && len(live) > p.MaxRecords {
		live = live[:p.MaxRecords] // the rest are the next addresses to take a blocked one's place
	}
	return live
}

// Failover calls fn with each address in order and stops at the first success,
// so a server-side client moves to the next address when one is blocked.
func Failover(ctx context.Context, ips []string, fn func(ctx context.Context, ip string) error) error {
	var last error
	for _, ip := range ips {
		if err := fn(ctx, ip); err == nil {
			return nil
		} else {
			last = err
		}
	}
	if last == nil {
		last = errors.New("pool: no address to try")
	}
	return last
}

func (p *Pool) written() []string {
	rec, ok := p.State.Get(Table, p.Name)
	if !ok {
		return nil
	}
	var ips []string
	_ = json.Unmarshal(rec["ips"], &ips)
	return ips
}

func (p *Pool) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// Tick checks the masters and, when the live set differs from the records and
// this master holds the writer's lease, writes it. The pool is never emptied:
// with no master answering, clients are better off with the last good records.
func (p *Pool) Tick(ctx context.Context) error {
	live := p.live(ctx)
	l := lease.Lease{
		Master: p.Master, State: p.State, Table: LeaseTable,
		TTL: orDefault(p.Lease, 2*time.Minute), Settle: orDefault(p.Settle, 5*time.Second),
	}
	holder := l.Holder(p.Name)
	if len(live) == 0 {
		return errors.New("pool: no master answers; records left as they are")
	}
	if slices.Equal(live, p.written()) {
		if holder == p.Master.Name() {
			_, err := l.Claim(ctx, p.Name) // the writer keeps its lease while all is well
			return err
		}
		return nil
	}
	ok, err := l.Claim(ctx, p.Name)
	if err != nil || !ok {
		return err
	}
	dns, zone, err := p.DNS(p.Name)
	if err != nil {
		return err
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 60
	}
	if err := dns.SetRecords(ctx, p.Name, "A", live, ttl); err != nil {
		return err
	}
	p.logf("pool: %s (%s) now %v", p.Name, zone, live)
	_, err = p.Master.Set(ctx, Table, p.Name, map[string]any{"ips": live})
	return err
}

// Run ticks every interval until ctx ends.
func (p *Pool) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := p.Tick(ctx); err != nil && ctx.Err() == nil {
			p.logf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Failing lists the addresses with failed checks, for the panel.
func (p *Pool) Failing() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := maps.Clone(p.failed)
	maps.DeleteFunc(out, func(_ string, n int) bool { return n == 0 })
	return out
}
