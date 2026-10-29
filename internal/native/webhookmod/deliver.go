package webhookmod

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Run is replaced by the delivery loop.
func (*Module) Run(ctx context.Context, _ module.Host) { <-ctx.Done() }
