package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vroute"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// BundleApplier puts a master's nodespec.Bundle into effect: Xray first,
// so the edge never points at inbounds that are not listening yet.
type BundleApplier struct {
	Xray     *xrayrun.Runner
	Edge     *edgerun.Runner
	Node     string
	Presence *presence.Map // nil: no network routing

	mu   sync.Mutex
	last *nodespec.Bundle
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
	return b.Edge.SetDrain(bundle.Drain)
}

func (b *BundleApplier) applyXray(ctx context.Context, bundle *nodespec.Bundle) error {
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
	b.Xray.API = bundle.API
	if err := b.Xray.Apply(ctx, x); err != nil {
		return err
	}
	b.last = bundle
	return nil
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
