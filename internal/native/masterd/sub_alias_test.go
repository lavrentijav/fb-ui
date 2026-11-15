package masterd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
)

// An old subscription id is an alias row; the public path must render the
// primary subscription behind it, not 404.
func TestSubscriptionAliasServesThePrimaryLinks(t *testing.T) {
	d := seedFleet(t)
	set(t, d, subauth.Table, "old-alice", map[string]any{
		"email": "alice", "enable": true, "aliasOf": "tok-alice",
	})
	req := httptest.NewRequest(http.MethodGet, "/sub/old-alice?format=plain", nil)
	req.SetPathValue("token", "old-alice")
	rec := httptest.NewRecorder()
	d.serveSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("alias sub: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Count(rec.Body.String(), "vless://"); got != 2 {
		t.Fatalf("%d links in:\n%s", got, rec.Body)
	}
}
