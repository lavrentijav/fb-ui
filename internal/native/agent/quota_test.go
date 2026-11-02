package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

func TestWithoutClientsDropsOnlyNamedEmails(t *testing.T) {
	cfg := `{"inbounds":[
	  {"tag":"vpn","settings":{"clients":[{"id":"a","email":"alice"},{"id":"b","email":"bob"}]}},
	  {"tag":"other","settings":{"clients":[{"id":"c","email":"carol"}]}}],
	 "outbounds":[{"tag":"direct"}]}`
	out, err := withoutClients([]byte(cfg), map[string]bool{"bob": true})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct{ Email string } `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 2 {
		t.Fatalf("inbounds %d", len(parsed.Inbounds))
	}
	if got := parsed.Inbounds[0].Settings.Clients; len(got) != 1 || got[0].Email != "alice" {
		t.Fatalf("vpn clients %+v", got)
	}
	if got := parsed.Inbounds[1].Settings.Clients; len(got) != 1 || got[0].Email != "carol" {
		t.Fatalf("other clients %+v", got)
	}
	// an empty set is a no-op that returns the same bytes
	same, err := withoutClients(out, nil)
	if err != nil || string(same) != string(out) {
		t.Fatalf("no-op changed the config: %v", err)
	}
	// an unknown email changes nothing
	again, err := withoutClients(out, map[string]bool{"nobody": true})
	if err != nil || string(again) != string(out) {
		t.Fatalf("unknown email changed the config: %v", err)
	}
}

type fakeSource struct{ used map[string]int64 }

func (f fakeSource) Used(_ context.Context, email string) (int64, bool, error) {
	n, ok := f.used[email]
	return n, ok, nil
}

type fakeTarget struct {
	quotas   map[string]nodespec.Quota
	disabled []string
	calls    int
	err      error
}

func (f *fakeTarget) Quotas() map[string]nodespec.Quota { return f.quotas }
func (f *fakeTarget) SetDisabled(_ context.Context, emails []string) error {
	f.calls++
	f.disabled = emails
	return f.err
}

func TestQuotaStopsSpentAndExpiredClients(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	target := &fakeTarget{quotas: map[string]nodespec.Quota{
		"spent":     {TotalBytes: 100},
		"under":     {TotalBytes: 100},
		"unlimited": {},
		"expired":   {TotalBytes: 0, ExpiresAt: now.UnixMilli() - 1},
		"later":     {ExpiresAt: now.UnixMilli() + 60_000},
	}}
	q := &Quota{
		Source: fakeSource{used: map[string]int64{"spent": 150, "under": 99, "unlimited": 1 << 40}},
		Target: target,
		Now:    func() time.Time { return now },
	}
	got := q.Tick(context.Background())
	if strings.Join(got, ",") != "expired,spent" {
		t.Fatalf("stopped %v", got)
	}
	if strings.Join(target.disabled, ",") != "expired,spent" {
		t.Fatalf("applied %v", target.disabled)
	}

	// an unchanged decision applies nothing a second time
	before := target.calls
	q.Tick(context.Background())
	if target.calls != before {
		t.Fatalf("a repeated tick re-applied the same set (%d -> %d)", before, target.calls)
	}

	// traffic reset and the decision is withdrawn
	q.Source = fakeSource{used: map[string]int64{"spent": 0, "under": 0, "unlimited": 0}}
	now = now.Add(30 * time.Second) // "later" still fine, "expired" still expired
	got = q.Tick(context.Background())
	if strings.Join(got, ",") != "expired" {
		t.Fatalf("after reset %v", got)
	}
}

func TestQuotaSurvivesAStoreError(t *testing.T) {
	target := &fakeTarget{quotas: map[string]nodespec.Quota{"alice": {TotalBytes: 10}}}
	q := &Quota{Source: errSource{}, Target: target, Now: func() time.Time { return time.Unix(0, 0) }}
	if got := q.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("an unreadable counter stopped %v", got)
	}
}

type errSource struct{}

func (errSource) Used(context.Context, string) (int64, bool, error) {
	return 0, false, errors.New("db down")
}
