package routing_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/routing"
)

func TestValidateEntry(t *testing.T) {
	good := map[string][]string{
		"domain": {"example.com", "geosite:category-ads-all", "regexp:\\.ru$", "full:a.example", "domain:x.org", "ext:geosite_RU.dat:ru"},
		"ip":     {"1.2.3.4", "10.0.0.0/8", "2001:db8::/32", "geoip:ru"},
		"asn":    {"AS13335", "as13335", "13335"},
	}
	for kind, entries := range good {
		for _, e := range entries {
			if err := routing.ValidateEntry(kind, e); err != nil {
				t.Errorf("%s %q refused: %v", kind, e, err)
			}
		}
	}
	bad := map[string][]string{
		"domain": {"a b", "x,y", "path/like", "weird:thing", "geosite:"},
		"ip":     {"not-an-ip", "geoip:", "1.2.3.4/99"},
		"asn":    {"", "AS", "AS0", "ASx", "1.1.1.1", "AS99999999999"},
	}
	for kind, entries := range bad {
		for _, e := range entries {
			if routing.ValidateEntry(kind, e) == nil {
				t.Errorf("%s %q must be refused", kind, e)
			}
		}
	}
}

func TestNormalizeASN(t *testing.T) {
	for in, want := range map[string]string{"13335": "AS13335", "as13335": "AS13335", " AS13335 ": "AS13335"} {
		if got, err := routing.NormalizeASN(in); err != nil || got != want {
			t.Fatalf("NormalizeASN(%q) = %q, %v", in, got, err)
		}
	}
}

func TestMatch(t *testing.T) {
	dom := []string{"domain:yandex.ru", "full:a.example", `regexp:\.su$`, "geosite:vk", "tracker"}
	for name, want := range map[string]bool{"mail.yandex.ru": true, "yandex.ru": true, "notyandex.ru": false, "a.example": true, "b.a.example": false, "site.su": true, "my-tracker.net": true, "google.com": false} {
		if hit, _ := routing.Match("domain", name, dom); hit != want {
			t.Errorf("domain %s: got %v", name, hit)
		}
	}
	if hit, maybe := routing.Match("domain", "vk.com", []string{"geosite:vk"}); hit || !maybe {
		t.Error("geosite must be undecided")
	}
	if hit, _ := routing.Match("ip", "1.0.0.7", []string{"1.0.0.0/24"}); !hit {
		t.Error("CIDR must match")
	}
	if hit, maybe := routing.Match("ip", "8.8.8.8", []string{"geoip:ru"}); hit || !maybe {
		t.Error("geoip must be undecided")
	}
	if hit, _ := routing.Match("ip", "2001:db8::1", []string{"2001:db8::/32"}); !hit {
		t.Error("IPv6 CIDR must match")
	}
}

func TestResolveASN(t *testing.T) {
	fetch := func(_ context.Context, url string) ([]byte, error) {
		switch {
		case strings.HasSuffix(url, "AS1"):
			return []byte(`{"data":{"prefixes":[{"prefix":"1.0.0.0/24"},{"prefix":"2001:db8::/32"},{"prefix":"junk"}]}}`), nil
		case strings.HasSuffix(url, "AS2"):
			return []byte(`{"data":{"prefixes":[{"prefix":"1.0.0.0/24"},{"prefix":"2.0.0.0/16"}]}}`), nil
		case strings.HasSuffix(url, "AS3"):
			return []byte(`{"data":{"prefixes":[]}}`), nil
		}
		return nil, errors.New("HTTP 500")
	}
	got, skipped, err := routing.ResolveASN(context.Background(), fetch, []string{"AS1", "AS2", "AS3"})
	if err != nil || skipped != 1 || strings.Join(got, ",") != "1.0.0.0/24,2001:db8::/32,2.0.0.0/16" {
		t.Fatalf("got %v skipped=%d err=%v", got, skipped, err)
	}
	if _, _, err := routing.ResolveASN(context.Background(), fetch, []string{"AS1", "AS9"}); err == nil {
		t.Fatal("one failed lookup must fail the whole resolve so the old prefixes stay")
	}
}

func TestParseListReadsPublishedShapes(t *testing.T) {
	body := []byte("# comment\n0.0.0.0 ads.example.com\n127.0.0.1 localhost\n||tracker.example.net^\n! adblock note\nPlain.Example.org.\nexample.com # trailing\nbad entry here\nregexp:\\.ru$\n")
	got, skipped := routing.ParseList(routing.KindDomain, body)
	want := []string{"ads.example.com", "tracker.example.net", "plain.example.org", "example.com", `regexp:\.ru$`}
	if strings.Join(got, "|") != strings.Join(want, "|") || skipped != 1 {
		t.Fatalf("got %v skipped=%d", got, skipped)
	}
	ips, skipped := routing.ParseList(routing.KindIP, []byte("1.2.3.0/24\n1.2.3.0/24\nnot-an-ip\n2001:db8::/32\n"))
	if strings.Join(ips, "|") != "1.2.3.0/24|2001:db8::/32" || skipped != 1 {
		t.Fatalf("ips %v skipped=%d", ips, skipped)
	}
}
