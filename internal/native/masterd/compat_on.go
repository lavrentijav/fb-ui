//go:build !no_compat

package masterd

import (
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/compat"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// withLegacy adds the old /panel/api surface next to /api/v1.
func withLegacy(mods []module.Module, api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api)
	mux.Handle("/panel/api/", compat.Handler(mods, api))
	return mux
}
