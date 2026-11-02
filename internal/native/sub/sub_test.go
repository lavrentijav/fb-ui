package sub_test

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
)

const uid = "11111111-1111-4111-8111-111111111111"

func specs() []nodespec.Spec {
	return []nodespec.Spec{
		{Node: "ru4", Inbounds: []nodespec.Inbound{
			{Tag: "vpn", Host: "ru.lab.test", Path: "/ws", Clients: []nodespec.Client{
				{Email: "alice", ID: uid}, {Email: "bob", ID: uid},
			}},
			{Tag: "hidden", Path: "/h", Clients: []nodespec.Client{{Email: "alice", ID: uid}}},
		}},
		{Node: "de2", Inbounds: []nodespec.Inbound{
			{Tag: "vpn", Host: "de.lab.test", Path: "/ws", Clients: []nodespec.Client{{Email: "alice", ID: uid}}},
		}},
	}
}

func TestLinksBelongToTheClient(t *testing.T) {
	got := sub.Links(specs(), "alice")
	if len(got) != 2 {
		t.Fatalf("alice gets %v", got)
	}
	if !strings.Contains(got[0], "@de.lab.test:443?") || !strings.Contains(got[1], "@ru.lab.test:443?") {
		t.Fatalf("order or hosts: %v", got)
	}
	if links := sub.Links(specs(), "bob"); len(links) != 1 {
		t.Fatalf("bob gets %v", links)
	}
	if links := sub.Links(specs(), "nobody"); len(links) != 0 {
		t.Fatalf("a stranger gets %v", links)
	}
}

func TestLinkIsAWorkingWsTLSURL(t *testing.T) {
	raw := sub.Link(uid, sub.Endpoint{Address: "ru.lab.test", Path: "/ws-vpn", Remark: "ru4-vpn"})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "vless" || u.User.Username() != uid || u.Host != "ru.lab.test:443" {
		t.Fatalf("url %s", raw)
	}
	for k, want := range map[string]string{
		"encryption": "none", "security": "tls", "sni": "ru.lab.test",
		"type": "ws", "host": "ru.lab.test", "path": "/ws-vpn", "alpn": "http/1.1",
	} {
		if q.Get(k) != want {
			t.Fatalf("%s = %q, want %q in %s", k, q.Get(k), want, raw)
		}
	}
	if u.Fragment != "ru4-vpn" {
		t.Fatalf("remark %q", u.Fragment)
	}
}

func TestBase64KeepsOneLinkPerLine(t *testing.T) {
	body := sub.Base64([]string{"vless://a", "vless://b"})
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "vless://a\nvless://b" {
		t.Fatalf("body %q", raw)
	}
}

func TestHostsOverrideTheEndpoint(t *testing.T) {
	hosts := sub.Hosts{"ru4/vpn": {
		{Address: "cdn.example", SNI: "ru.lab.test", Port: 8443, Remark: "cdn"},
		{Address: "1.2.3.4", Security: "none"},
	}}
	got := sub.LinksWith(specs(), "alice", hosts)
	// de2 is untouched, ru4's inbound yields two advertised links
	if len(got) != 3 {
		t.Fatalf("links %v", got)
	}
	if !strings.Contains(got[1], "@cdn.example:8443?") || !strings.Contains(got[1], "sni=ru.lab.test") ||
		!strings.Contains(got[1], "path=%2Fws") || !strings.HasSuffix(got[1], "cdn") {
		t.Fatalf("cdn link %s", got[1])
	}
	if !strings.Contains(got[2], "@1.2.3.4:443?") || !strings.Contains(got[2], "security=none") ||
		strings.Contains(got[2], "sni=") {
		t.Fatalf("plain link %s", got[2])
	}
}

func TestLinkCarriesTheExtraParametersAndTheTransport(t *testing.T) {
	raw := sub.Link(uid, sub.Endpoint{
		Address: "ru.lab.test", Network: "xhttp", Path: "/x", Remark: "ru4",
		Extra: map[string]string{"userinfo": "download=5; total=10", "providerid": "p1", "node": "ru4"},
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("type") != "xhttp" {
		t.Fatalf("type = %q", q.Get("type"))
	}
	for k, want := range map[string]string{
		"fp": "chrome", "headerType": "none", "allowInsecure": "0",
		"userinfo": "download=5; total=10", "providerid": "p1", "node": "ru4",
	} {
		if q.Get(k) != want {
			t.Fatalf("%s = %q, want %q in %s", k, q.Get(k), want, raw)
		}
	}
}

func TestAnExtraCannotBreakTheTransport(t *testing.T) {
	raw := sub.Link(uid, sub.Endpoint{
		Address: "ru.lab.test", Path: "/ws", Security: "none",
		Extra: map[string]string{"security": "reality", "sni": "evil"},
	})
	q, _ := url.Parse(raw)
	if q.Query().Get("security") != "none" {
		t.Fatalf("an extra overrode security: %s", raw)
	}
}

func TestOptionsNameTheLinksAndAddParameters(t *testing.T) {
	got := sub.LinksWithOpts(specs(), "alice", sub.Options{
		Remark: func(node string, ib nodespec.Inbound) string { return node + "/" + ib.Tag },
		Extra: func(node string, ib nodespec.Inbound) map[string]string {
			return map[string]string{"node": node, "inbound": ib.Tag}
		},
	})
	if len(got) != 2 {
		t.Fatalf("links %v", got)
	}
	for _, raw := range got {
		u, _ := url.Parse(raw)
		if !strings.Contains(u.Fragment, "/") {
			t.Fatalf("remark not templated: %s", raw)
		}
		if u.Query().Get("node") == "" || u.Query().Get("inbound") == "" {
			t.Fatalf("extras missing: %s", raw)
		}
	}
}

func TestHostsKeepTheTemplatedRemark(t *testing.T) {
	got := sub.LinksWithOpts(specs(), "alice", sub.Options{
		Hosts:  sub.Hosts{"ru4/vpn": {{Address: "cdn.example", Remark: "cdn"}}},
		Remark: func(node string, ib nodespec.Inbound) string { return "S-" + node },
	})
	// de2 first (sorted), then ru4's advertised link
	if len(got) != 2 {
		t.Fatalf("links %v", got)
	}
	if !strings.HasSuffix(got[1], "#S-ru4%20cdn") {
		t.Fatalf("advertised remark %s", got[1])
	}
}
