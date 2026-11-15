package enroll_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
)

type mem struct {
	rows map[string]map[string]any
}

func newMem() *mem { return &mem{rows: map[string]map[string]any{}} }

func (m *mem) Set(_ context.Context, _, key string, fields map[string]any) error {
	m.rows[key] = fields
	return nil
}

func (m *mem) Delete(_ context.Context, _, key string) error {
	delete(m.rows, key)
	return nil
}

func (m *mem) Get(_ string, key string) (map[string]json.RawMessage, bool) {
	f, ok := m.rows[key]
	if !ok {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	for k, v := range f {
		out[k], _ = json.Marshal(v)
	}
	return out, true
}

func TestTokenCarriesTheFingerprint(t *testing.T) {
	ca, _ := fleetca.New("fleet")
	tok := enroll.Token("secret", ca)
	secret, pin, ok := enroll.Parse(tok)
	if !ok || secret != "secret" || pin != enroll.Pin(ca) {
		t.Fatalf("parse %q -> %q %q %v", tok, secret, pin, ok)
	}
	for _, bad := range []string{"", "enr1_x", "nope_secret_pin", "enr1__pin", "enr1_s_pin_extra"} {
		if _, _, ok := enroll.Parse(bad); ok {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

type harness struct {
	srv *httptest.Server
	ca  *fleetca.CA
	st  *mem
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ca, err := fleetca.New("fleet")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{ca: ca, st: newMem(), now: time.Unix(1_700_000_000, 0)}
	h.srv = httptest.NewServer(enroll.Handler(ca, h.st, h.st, func() time.Time { return h.now }))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) mint(t *testing.T, secret, node string, ttl time.Duration) string {
	t.Helper()
	if err := enroll.Mint(context.Background(), h.st, secret, node, ttl, h.now); err != nil {
		t.Fatal(err)
	}
	return enroll.Token(secret, h.ca)
}

func TestRedeemIssuesOneTime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tok := h.mint(t, "s3cret", "ru4", time.Hour)

	res, err := enroll.Exchange(ctx, h.srv.URL, tok, "ru4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Node != "ru4" || !strings.Contains(res.Cert, "CERTIFICATE") || !strings.Contains(res.Key, "PRIVATE KEY") ||
		!strings.Contains(res.CA, "CERTIFICATE") {
		t.Fatalf("result %+v", res)
	}
	if _, err := enroll.Exchange(ctx, h.srv.URL, tok, "ru4", nil); err == nil {
		t.Fatal("the token was redeemed twice")
	}
}

func TestWrongNodeIsRefused(t *testing.T) {
	h := newHarness(t)
	tok := h.mint(t, "s", "ru4", time.Hour)
	if _, err := enroll.Exchange(context.Background(), h.srv.URL, tok, "de2", nil); err == nil ||
		!strings.Contains(err.Error(), "another node") {
		t.Fatalf("another node: %v", err)
	}
}

func TestExpiredTokenIsDropped(t *testing.T) {
	h := newHarness(t)
	tok := h.mint(t, "x", "ru4", time.Minute)
	h.now = h.now.Add(2 * time.Minute)
	if _, err := enroll.Exchange(context.Background(), h.srv.URL, tok, "ru4", nil); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry: %v", err)
	}
	if _, ok := h.st.Get(enroll.Table, enroll.Hash("x")); ok {
		t.Fatal("an expired token stayed in the state")
	}
}

// A master that serves another CA (a man in the middle) is refused.
func TestForeignCAIsRefused(t *testing.T) {
	h := newHarness(t)
	other, _ := fleetca.New("other")
	_ = h.mint(t, "s", "ru4", time.Hour)
	// the token is pinned to other's CA, the server has h.ca
	tok := enroll.Token("s", other)
	if _, err := enroll.Exchange(context.Background(), h.srv.URL, tok, "ru4", nil); err == nil {
		t.Fatal("accepted a certificate from another CA")
	} else if !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("error %v", err)
	}
}

func TestUnknownAndMalformedTokens(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "garbage", enroll.Token("never-minted", h.ca)} {
		if _, err := enroll.Exchange(context.Background(), h.srv.URL, tok, "ru4", nil); err == nil {
			t.Fatalf("%q was redeemed", tok)
		}
	}
}
