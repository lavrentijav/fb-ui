//go:build !no_audit && !no_webhook && !no_routing

package masterd

import (
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func TestDefaultBuildRegistersTheOptionalModules(t *testing.T) {
	var names []string
	for _, m := range module.All() {
		names = append(names, m.Name())
	}
	for _, want := range []string{"audit", "webhooks", "routing"} {
		if !slices.Contains(names, want) {
			t.Fatalf("module %q missing from the default build: %v", want, names)
		}
	}
}
