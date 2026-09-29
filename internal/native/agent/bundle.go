package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// BundleApplier puts a master's nodespec.Bundle into effect: Xray first,
// so the edge never points at inbounds that are not listening yet.
type BundleApplier struct {
	Xray *xrayrun.Runner
	Edge *edgerun.Runner
}

func (b *BundleApplier) Apply(ctx context.Context, st desired.State) error {
	var bundle nodespec.Bundle
	if err := json.Unmarshal(st.Config, &bundle); err != nil {
		return fmt.Errorf("agent: bundle: %w", err)
	}
	b.Xray.API = bundle.API
	if err := b.Xray.Apply(ctx, bundle.Xray); err != nil {
		return err
	}
	return b.Edge.Apply(bundle.Edge, bundle.Certs)
}
