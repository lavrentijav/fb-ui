//go:build netlab

package netlab

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

func exitSpec(l *lab, t *testing.T, node string) nodespec.Spec {
	name := node + ".lab.firewalk.test"
	return nodespec.Spec{
		Node: node,
		Inbounds: []nodespec.Inbound{{
			Tag: "cascade", Port: 12443, Network: "ws", Path: "/ws-update", Host: name,
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: serversID}},
		}},
		Certs: []nodespec.Cert{l.cert(t, name)}, DirectAllow: []string{"10.0.0.0/16"},
	}
}

func hop(node string) nodespec.Hop {
	name := node + ".lab.firewalk.test"
	return nodespec.Hop{Address: name, Port: 443, ID: serversID, Network: "ws", Path: "/ws-update", SNI: name, Host: name}
}

// Exits de1 and de2 are one group behind proxy_de on ru4; stopping the exit
// in use moves traffic to the other without touching ru4's config.
func TestGroupBalancerFailsOver(t *testing.T) {
	l := newLab(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	exits := map[string]*os.Process{}
	for _, n := range []string{"de1", "de2"} {
		cmd := l.node(t, exitSpec(l, t, n))
		exits[ipOf(n)] = cmd.Process
	}
	l.node(t, nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.lab.firewalk.test",
			Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Exits:       []nodespec.Exit{{Country: "de", Hops: []nodespec.Hop{hop("de1"), hop("de2")}}},
		Probe:       nodespec.Probe{URL: "http://web.lab.test/ping", Interval: "1s"},
		Certs:       []nodespec.Cert{l.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	})
	l.client(t, map[int]string{1080: "/ws-de"})

	var first string
	eventually(t, 30*time.Second, "traffic leaves through a de exit", func() (bool, string) {
		ok, got := exitsVia(1080)()
		first = strings.TrimPrefix(got, "/ip ")
		return ok && (first == ipOf("de1") || first == ipOf("de2")), got
	})
	t.Logf("balancer picked %s; stopping it", first)
	_ = exits[first].Signal(syscall.SIGTERM)
	start := time.Now()
	eventually(t, 30*time.Second, "traffic moves to the other exit", func() (bool, string) {
		ok, got := exitsVia(1080)()
		return ok && got != "/ip "+first, got
	})
	t.Logf("moved in %s", time.Since(start).Round(100*time.Millisecond))
}
