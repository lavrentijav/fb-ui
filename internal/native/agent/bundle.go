package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vdns"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vroute"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// BundleApplier puts a master's nodespec.Bundle into effect: Xray first,
// so the edge never points at inbounds that are not listening yet.
type BundleApplier struct {
	Xray     *xrayrun.Runner
	Edge     *edgerun.Runner
	DNS      *vdns.Manager // nil: no internal names
	Node     string
	Presence *presence.Map // nil: no network routing

	mu       sync.Mutex
	last     *nodespec.Bundle
	bans     []string
	disabled []string // emails stopped locally (spent quota, expiry)
}

func (b *BundleApplier) Apply(ctx context.Context, st desired.State) error {
	var bundle nodespec.Bundle
	if err := json.Unmarshal(st.Config, &bundle); err != nil {
		return fmt.Errorf("agent: bundle: %w", err)
	}
	if err := b.applyXray(ctx, &bundle); err != nil {
		return err
	}
	if err := b.Edge.Apply(bundle.Edge, bundle.Certs); err != nil {
		return err
	}
	if err := b.Edge.SetDrain(bundle.Drain); err != nil {
		return err
	}
	b.applyDNS(&bundle)
	return nil
}

// applyDNS follows the bundle's names and per-network resolvers.
func (b *BundleApplier) applyDNS(bundle *nodespec.Bundle) {
	if b.DNS == nil {
		return
	}
	if bundle.VNet == nil {
		b.DNS.Update("", nil, nil)
		return
	}
	names := make([]vdns.Name, 0, len(bundle.VNet.Members))
	for _, m := range bundle.VNet.Members {
		if m.Name != "" {
			names = append(names, vdns.Name{Name: m.Name, IP: m.IP, Network: m.Network})
		}
	}
	rs := make([]vdns.Resolver, 0, len(bundle.VNet.Resolvers))
	for _, r := range bundle.VNet.Resolvers {
		rs = append(rs, vdns.Resolver{Network: r.Network, Port: r.Port})
	}
	b.DNS.Update(bundle.VNet.Zone, names, rs)
}

func (b *BundleApplier) applyXray(ctx context.Context, bundle *nodespec.Bundle) error {
	if b.Xray == nil {
		return nil // relay: this node forwards, it does not run a core
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var where map[string]string
	if b.Presence != nil {
		where = b.Presence.Where()
	}
	x, err := vroute.Patch(bundle.Xray, bundle.VNet, where, b.Node)
	if err != nil {
		return err
	}
	if x, err = vroute.Bans(x, b.bans); err != nil {
		return err
	}
	if len(b.disabled) > 0 {
		off := make(map[string]bool, len(b.disabled))
		for _, email := range b.disabled {
			off[email] = true
		}
		if x, err = withoutClients(x, off); err != nil {
			return err
		}
	}
	b.Xray.API = bundle.API
	if err := b.Xray.Apply(ctx, x); err != nil {
		return err
	}
	b.last = bundle
	return nil
}

// Quotas is each user's traffic allowance and expiry from the last bundle.
func (b *BundleApplier) Quotas() map[string]nodespec.Quota {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil || len(b.last.Quotas) == 0 {
		return nil
	}
	return maps.Clone(b.last.Quotas)
}

// Disabled is the set of emails this node stopped locally.
func (b *BundleApplier) Disabled() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.disabled)
}

// SetDisabled removes the named emails from the running config (an empty list
// restores them). An unchanged set does nothing, so the quota loop is cheap.
func (b *BundleApplier) SetDisabled(ctx context.Context, emails []string) error {
	sorted := slices.Sorted(slices.Values(emails))
	b.mu.Lock()
	if slices.Equal(b.disabled, sorted) {
		b.mu.Unlock()
		return nil
	}
	b.disabled = sorted
	last := b.last
	b.mu.Unlock()
	if last == nil {
		return nil
	}
	return b.applyXray(ctx, last)
}

// Limits is each user's IP limit from the last bundle.
func (b *BundleApplier) Limits() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil || len(b.last.Limits) == 0 {
		return nil
	}
	return maps.Clone(b.last.Limits)
}

// SetBans blocks the given source addresses; an unchanged set does nothing.
func (b *BundleApplier) SetBans(ctx context.Context, bans []string) error {
	sorted := slices.Sorted(slices.Values(bans))
	b.mu.Lock()
	if slices.Equal(b.bans, sorted) {
		b.mu.Unlock()
		return nil
	}
	b.bans = sorted
	last := b.last
	b.mu.Unlock()
	if last == nil {
		return nil
	}
	return b.applyXray(ctx, last)
}

// Repatch applies the last bundle again with the fleet's current presence.
func (b *BundleApplier) Repatch(ctx context.Context) error {
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	if last == nil || last.VNet == nil {
		return nil
	}
	return b.applyXray(ctx, last)
}
