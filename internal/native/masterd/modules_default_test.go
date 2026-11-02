//go:build !no_access && !no_subs && !no_audit && !no_webhook && !no_routing && !no_certs && !no_ui && !no_templates && !no_hosts && !no_settings && !no_notify && !no_clients && !no_usage && !no_inbounds && !no_dashboard && !no_groups && !no_hosts

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
	for _, want := range []string{"access", "subs", "audit", "webhooks", "routing", "certs", "ui", "templates", "hosts", "settings", "notifymod", "clients", "usage", "inbounds", "dashboard", "groups"} {
		if !slices.Contains(names, want) {
			t.Fatalf("module %q missing from the default build: %v", want, names)
		}
	}
}
