//go:build no_audit && no_webhook && no_compat && no_routing && no_certs

package masterd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

// A build that cuts every optional module carries none of them: nothing is
// registered, and the legacy surface is not mounted.
func TestTaggedBuildHasNoOptionalRoutes(t *testing.T) {
	if n := len(module.All()); n != 0 {
		t.Fatalf("%d modules registered in a tagged build", n)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	withLegacy(nil, api).ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/webhooks/list", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("the legacy surface answered itself (%d); it should be absent", rec.Code)
	}
}
