//go:build no_compat

package masterd

import (
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// withLegacy is the identity: a build without compat serves /api/v1 only.
func withLegacy(_ []module.Module, api http.Handler) http.Handler { return api }
