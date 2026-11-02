package submeta_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/submeta"
)

func decode(t *testing.T, v string) string {
	t.Helper()
	if !strings.HasPrefix(v, "base64:") {
		t.Fatalf("value is not base64: %q", v)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "base64:"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestQuotaStringMatchesTheDeFactoForm(t *testing.T) {
	q := submeta.Quota{Upload: 1, Download: 2, Total: 3, Expire: 4}
	if got, want := q.String(), "upload=1; download=2; total=3; expire=4"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestHeadersCarryTheMetadataInTheFormsClientsRead(t *testing.T) {
	in := submeta.Info{
		Title:       "🇩🇪 Berlin",
		UpdateHours: 6,
		SupportURL:  "https://t.me/support",
		WebPageURL:  "https://panel.example/sub/abc",
		Announce: submeta.Announce{
			Text: "Use the new app", URL: "https://example.com/app",
			Color: "warning", ButtonText: "Download", ButtonLink: "https://example.com/dl",
		},
		Quota:      submeta.Quota{Upload: 10, Download: 20, Total: 100, Expire: 1700000000},
		RefillDate: 1800000000,
		ProviderID: "provider-7",
		BackupURLs: []string{"https://mirror.example/sub/abc", "https://mirror.example/sub/abc"},
		Domains:    []string{"a.example", "b.example"},
		IPs:        []string{"10.0.0.1", "10.0.0.2"},
		HwidLimit:  3,
		Signature:  "deadbeef",
	}
	h := in.Headers()

	cases := map[string]string{
		"Subscription-Userinfo":           "upload=10; download=20; total=100; expire=1700000000",
		"Profile-Update-Interval":         "6",
		"Support-Url":                     "https://t.me/support",
		"Profile-Web-Page-Url":            "https://panel.example/sub/abc",
		"Announce-Url":                    "https://example.com/app",
		"Sub-Info-Color":                  "red", // warning is Happ's red
		"Sub-Info-Text":                   "Use the new app",
		"Sub-Info-Button-Text":            "Download",
		"Sub-Info-Button-Link":            "https://example.com/dl",
		"Subscription-Refill-Date":        "1800000000",
		"ProviderID":                      "provider-7",
		"Profile-Backup-Urls":             "https://mirror.example/sub/abc",
		"Fallback-Url":                    "https://mirror.example/sub/abc",
		"X-Subscription-Fallback-Domains": "a.example, b.example",
		"X-Subscription-Fallback-IPs":     "10.0.0.1, 10.0.0.2",
		"X-Hwid-Limit":                    "3",
		"X-Subscription-Signature":        "deadbeef",
	}
	for k, want := range cases {
		if got := h[k]; got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	if got := decode(t, h["Profile-Title"]); got != "🇩🇪 Berlin" {
		t.Fatalf("profile title %q", got)
	}
	if got := decode(t, h["Announce"]); got != "Use the new app" {
		t.Fatalf("announce %q", got)
	}
	if !strings.Contains(h["Content-Disposition"], "filename*=UTF-8''") {
		t.Fatalf("content disposition %q", h["Content-Disposition"])
	}
}

func TestEmptyInfoSendsOnlyTheQuota(t *testing.T) {
	h := submeta.Info{}.Headers()
	if len(h) != 1 || h["Subscription-Userinfo"] != "upload=0; download=0; total=0; expire=0" {
		t.Fatalf("an empty subscription sent %v", h)
	}
}

func TestAnUnlimitedSubscriptionStillSendsItsQuota(t *testing.T) {
	h := submeta.Info{Quota: submeta.Quota{Download: 5}}.Headers()
	if h["Subscription-Userinfo"] != "upload=0; download=5; total=0; expire=0" {
		t.Fatalf("%q", h["Subscription-Userinfo"])
	}
}

func TestServerUserinfoIsBase64JSONKeyedByRemark(t *testing.T) {
	in := submeta.Info{
		Servers: map[string]submeta.Quota{
			"de1-vpn": {Upload: 1, Download: 2, Total: 10, Expire: 20},
			"ru2-vpn": {Upload: 3, Download: 4, Total: 10, Expire: 20},
		},
	}
	h := in.Headers()["Server-Userinfo"]
	if !strings.HasPrefix(h, "base64:") {
		t.Fatalf("%q", h)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "base64:"))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]string
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["de1-vpn"] != "upload=1; download=2; total=10; expire=20" {
		t.Fatalf("de1: %v", obj)
	}
	if len(obj) != 2 {
		t.Fatalf("servers: %v", obj)
	}
}

func TestBodyLinesAndPlainCarryTheLinks(t *testing.T) {
	in := submeta.Info{
		Title: "My VPN", UpdateHours: 12, Quota: submeta.Quota{Download: 7, Total: 10},
		Announce:   submeta.Announce{Text: "hello", Color: "green"},
		ProviderID: "p1",
	}
	body := in.Plain([]string{"vless://a@b:443?x=1#de", "vless://c@d:443?x=2#ru"})
	for _, want := range []string{
		"#profile-title: base64:", "#profile-update-interval: 12",
		"#subscription-userinfo: upload=0; download=7; total=10; expire=0",
		"#sub-info-color: green", "#providerid: p1",
		"vless://a@b:443?x=1#de", "vless://c@d:443?x=2#ru",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	if !strings.HasSuffix(body, "\n") {
		t.Fatal("body should end with a newline")
	}
}

func TestBodyLinesCannotInjectALine(t *testing.T) {
	in := submeta.Info{Announce: submeta.Announce{Text: "hi\r\n#subscription-userinfo: upload=9"}}
	for _, line := range in.BodyLines() {
		if strings.Contains(line, "\r") || strings.Count(line, "\n") > 0 {
			t.Fatalf("a value broke the line: %q", line)
		}
	}
}
