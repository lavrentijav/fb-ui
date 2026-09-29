//go:build netlab

package netlab

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// Maintenance on ru4: a download already running finishes, new tunnels are
// refused, and lifting maintenance lets them in again. Only the edge
// changes; Xray keeps running, or the open download would break.
func TestDrainKeepsOpenTunnels(t *testing.T) {
	l := newLab(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	spec := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "ru", Port: 12054, Network: "ws", Path: "/ws-ru", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Certs:       []nodespec.Cert{l.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	}
	ru4 := l.node(t, spec)
	l.client(t, map[int]string{1081: "/ws-ru"})
	if _, err := getVia(1081, "http://web.lab.test/ip"); err != nil {
		t.Fatalf("before maintenance: %v", err)
	}

	slow := make(chan string, 1)
	go func() {
		got, err := getVia(1081, "http://web.lab.test/slow")
		if err != nil {
			got = "error: " + err.Error()
		}
		slow <- got
	}()
	time.Sleep(time.Second) // the download is under way

	spec.Drain = nodespec.Drain{On: true, Idle: "1m"}
	l.setBundle(t, ru4, spec)
	eventually(t, 10*time.Second, "new tunnels refused", func() (bool, string) {
		got, err := getVia(1081, "http://web.lab.test/ip")
		if err == nil {
			return false, "answered " + got
		}
		return true, err.Error()
	})
	if got := <-slow; got != "chunk0;chunk1;chunk2;chunk3;chunk4;chunk5;chunk6;chunk7;" {
		t.Fatalf("download open before maintenance ended as %q", got)
	}

	spec.Drain = nodespec.Drain{}
	l.setBundle(t, ru4, spec)
	eventually(t, 10*time.Second, "tunnels accepted again", func() (bool, string) {
		ok, got := exitsVia(1081)()
		return ok, got
	})
}
