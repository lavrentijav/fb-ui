package webhookmod

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// Tick runs one delivery pass.
func (m *Module) Tick(ctx context.Context, h module.Host) (int, error) { return m.tick(ctx, h) }
