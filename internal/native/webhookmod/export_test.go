package webhookmod

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// SetURLCheck replaces the public-address check and returns the undo.
func SetURLCheck(f func(string) error) func() {
	old := checkURL
	checkURL = f
	return func() { checkURL = old }
}

// Tick runs one delivery pass.
func (m *Module) Tick(ctx context.Context, h module.Host) (int, error) { return m.tick(ctx, h) }
