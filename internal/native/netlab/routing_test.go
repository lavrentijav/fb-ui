//go:build netlab

package netlab

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// A routing rule overrides the inbound's route for the traffic it names, on the
// inbounds it names, and leaves everything else on the default (the proxy).
func TestRoutingRulesOverrideTheDefaultRoute(t *testing.T) {
	l := newLab(t)
	webCert := l.cert(t, "mail.lab.firewalk.test")
	wdir := filepath.Join(l.dir, "web")
	os.MkdirAll(wdir, 0o700)
	os.WriteFile(filepath.Join(wdir, "cert.pem"), []byte(webCert.CertPEM), 0o600)
	os.WriteFile(filepath.Join(wdir, "key.pem"), []byte(webCert.KeyPEM), 0o600)
	spawn(t, "web", "web", "NETLAB_CERT="+filepath.Join(wdir, "cert.pem"), "NETLAB_KEY="+filepath.Join(wdir, "key.pem"))
	waitTCP(t, ipOf("web")+":80")

	l.node(t, nodespec.Spec{
		Node: "de2",
		Inbounds: []nodespec.Inbound{{
			Tag: "cascade", Port: 12443, Network: "ws", Path: "/ws-update", Host: "de2.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: serversID}},
		}},
		Certs:       []nodespec.Cert{l.cert(t, "de2.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	})
	l.node(t, nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{Tag: "ruled", Port: 12053, Network: "ws", Path: "/ws-ruled", Host: "ru.lab.firewalk.test", Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
			{Tag: "plain", Port: 12054, Network: "ws", Path: "/ws-plain", Host: "ru.lab.firewalk.test", Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
			{Tag: "blocked", Port: 12055, Network: "ws", Path: "/ws-blocked", Host: "ru.lab.firewalk.test", Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
		},
		Rules: []nodespec.Rule{
			{Name: "site direct", Inbounds: []string{"ruled"}, Domains: []string{"full:web.lab.test"}, Route: nodespec.Direct},
			{Name: "site blocked", Inbounds: []string{"blocked"}, IPs: []string{ipOf("web") + "/32"}, Route: nodespec.Block},
		},
		Exits: []nodespec.Exit{{Country: "de", Hops: []nodespec.Hop{{
			Address: "de2.lab.firewalk.test", Port: 443, ID: serversID, Network: "ws", Path: "/ws-update",
			SNI: "de2.lab.firewalk.test", Host: "de2.lab.firewalk.test",
		}}}},
		Certs:       []nodespec.Cert{l.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	})
	l.client(t, map[int]string{1080: "/ws-ruled", 1081: "/ws-plain", 1082: "/ws-blocked"})

	t.Run("a domain rule sends the site direct from ru4", func(t *testing.T) {
		got, err := getVia(1080, "http://web.lab.test/ip")
		if err != nil || got != "/ip "+ipOf("ru4") {
			t.Fatalf("got %q %v, want ru4 (%s)", got, err, ipOf("ru4"))
		}
	})
	t.Run("an inbound without the rule keeps the proxy", func(t *testing.T) {
		got, err := getVia(1081, "http://web.lab.test/ip")
		if err != nil || got != "/ip "+ipOf("de2") {
			t.Fatalf("got %q %v, want de2 (%s)", got, err, ipOf("de2"))
		}
	})
	t.Run("an IP rule blocks the site on its inbound", func(t *testing.T) {
		if got, err := getVia(1082, "http://web.lab.test/ip"); err == nil {
			t.Fatalf("the blocked inbound answered %q", got)
		}
	})
}
