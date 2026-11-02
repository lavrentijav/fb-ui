//go:build netlab

package netlab

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// socksOn runs a socks client on host, tunnelling to ru4 as alice.
func (l *lab) socksOn(t *testing.T, host string, port int, path string) *exec.Cmd {
	t.Helper()
	cfg := fmt.Sprintf(`{"log":{"loglevel":"warning"},
	 "inbounds":[{"tag":"in","listen":"%s","port":%d,"protocol":"socks","settings":{"udp":false}}],
	 "outbounds":[{"tag":"out","protocol":"vless","settings":{"vnext":[{"address":"ru.lab.firewalk.test","port":443,
	   "users":[{"id":"%s","encryption":"none"}]}]},
	  "streamSettings":{"network":"ws","security":"tls",
	   "tlsSettings":{"serverName":"ru.lab.firewalk.test","alpn":["http/1.1"]},
	   "wsSettings":{"path":"%s","host":"ru.lab.firewalk.test"}}}]}`,
		ipOf(host), port, aliceID, path)
	f := filepath.Join(l.dir, host+".json")
	if err := os.WriteFile(f, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := spawnCmd(t, host, nil, l.xray, "run", "-c", f)
	waitTCP(t, fmt.Sprintf("%s:%d", ipOf(host), port))
	return cmd
}

// Two devices of one user with LimitIP 1: the device that connected first is
// banned on the node, the newer one keeps working. The node does this itself
// (fail2ban lives on the node), with no master involvement after the spec.
func TestIPLimitBansTheOlderDevice(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID, LimitIP: 1}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")

	f.socksOn(t, "alice", 1080, "/ws-vpn")
	via := func(host string, port int) (bool, string) {
		got, err := getViaFrom(host, port, "http://web.lab.test/ip")
		if err != nil {
			return false, err.Error()
		}
		return true, got
	}
	eventually(t, 60*time.Second, "the first device works", func() (bool, string) {
		ok, got := via("alice", 1080)
		return ok && got == "/ip "+ipOf("ru4"), got
	})

	time.Sleep(2 * time.Second) // so the two addresses get different timestamps
	f.socksOn(t, "client", 1081, "/ws-vpn")
	eventually(t, 60*time.Second, "the second device works", func() (bool, string) {
		ok, got := via("client", 1081)
		return ok && got == "/ip "+ipOf("ru4"), got
	})

	eventually(t, 60*time.Second, "the older device is banned", func() (bool, string) {
		ok, got := via("alice", 1080)
		if ok {
			return false, "still reaches the site: " + got
		}
		return true, got
	})
	if ok, got := via("client", 1081); !ok {
		t.Fatalf("the newer device was banned too: %s", got)
	}
}
