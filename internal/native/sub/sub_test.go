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
	raw := sub.Link(uid, "ru.lab.test", "/ws-vpn", "ru4-vpn")
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
