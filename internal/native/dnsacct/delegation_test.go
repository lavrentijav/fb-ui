package dnsacct_test

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/dnsacct"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func put(t *testing.T, h *modtest.Host, id string, acc dnsacct.Account) {
	t.Helper()
	if _, err := h.Set(context.Background(), dnsacct.Table, id, map[string]any{
		"type": acc.Type, "baseUrl": acc.BaseURL, "credential": acc.Credential, "zones": acc.Zones,
	}); err != nil {
		t.Fatal(err)
	}
}

// A zone can sit in two accounts (an old copy after a move). A challenge record
// must go to the account the internet asks, or the CA never sees it.
func TestAccountChoiceFollowsThePublicDelegation(t *testing.T) {
	h := modtest.NewHost("m1")
	put(t, h, "a1", dnsacct.Account{Type: "technitium", BaseURL: "https://ns1.boykisser.online:53443", Credential: "t", Zones: []string{"boy-kisser.ru", "boykisser.online"}})
	put(t, h, "a2", dnsacct.Account{Type: "cloudflare", Credential: "c", Zones: []string{"boy-kisser.ru"}})
	acc := &dnsacct.Accounts{State: h}

	calls := 0
	ns := []string{"aldo.ns.cloudflare.com"}
	defer dnsacct.SetLookupNS(func(string) []string { calls++; return ns })()

	if got, _ := acc.Best("s3.boy-kisser.ru"); got.Credential != "c" {
		t.Fatalf("a zone delegated to Cloudflare chose %q", got.Credential)
	}
	ns = []string{"ns1.boykisser.online"}
	if got, _ := acc.Best("s3.boy-kisser.ru"); got.Credential != "t" {
		t.Fatalf("a zone delegated to the Technitium host chose %q", got.Credential)
	}
	ns = nil // the lookup failed: keep the old rule (first account holding the longest zone)
	if got, _ := acc.Best("s3.boy-kisser.ru"); got.Credential != "t" {
		t.Fatalf("with no answer chose %q", got.Credential)
	}

	calls = 0
	if got, zone := acc.Best("s3.boykisser.online"); got.Credential != "t" || zone != "boykisser.online" || calls != 0 {
		t.Fatalf("a zone held by one account must not cost a lookup (calls=%d)", calls)
	}
}
