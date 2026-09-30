//go:build netlab

package netlab

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// xrayPID is the pid of the Xray running node's config, 0 if none.
func (l *lab) xrayPID(node string) int {
	want := filepath.Join(l.dir, node, "xray.json")
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if strings.Contains(string(cmd), want) && strings.Contains(string(cmd), "run") {
			return pid
		}
	}
	return 0
}

// Users, routes and inbounds change under a download through ru4, which ends
// whole with the same Xray; a new default outbound still restarts it.
func TestXrayChangesKeepTunnels(t *testing.T) {
	l := newLab(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	alice := []nodespec.Client{{Email: "alice", ID: aliceID}}
	spec := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{Tag: "ru", Port: 12054, Network: "ws", Path: "/ws-ru", Host: "ru.lab.firewalk.test", Route: nodespec.Direct, Clients: alice},
			{Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.lab.firewalk.test", Route: nodespec.Direct, Clients: alice},
		},
		Certs:       []nodespec.Cert{l.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")},
		DirectAllow: []string{"10.0.0.0/16"},
	}
	ru4 := l.node(t, spec)
	l.client(t, map[int]string{1081: "/ws-ru", 1082: "/ws-de", 1083: "/ws-x"})
	for _, port := range []int{1081, 1082} {
		if _, err := getVia(port, "http://web.lab.test/ip"); err != nil {
			t.Fatalf("before the change, %d: %v", port, err)
		}
	}
	if _, err := getVia(1083, "http://web.lab.test/ip"); err == nil {
		t.Fatal("/ws-x answered before it existed")
	}
	pid := l.xrayPID("ru4")
	if pid == 0 {
		t.Fatal("no xray for ru4")
	}

	slow := make(chan string, 1)
	go func() {
		got, err := getVia(1081, "http://web.lab.test/slow")
		if err != nil {
			got = "error: " + err.Error()
		}
		slow <- got
	}()
	time.Sleep(time.Second)

	spec.Inbounds[0].Clients = append(spec.Inbounds[0].Clients, nodespec.Client{Email: "bob", ID: "22222222-2222-4222-8222-222222222222"})
	spec.Inbounds[1].Route = nodespec.Block
	spec.Inbounds = append(spec.Inbounds, nodespec.Inbound{
		Tag: "x", Port: 12055, Network: "ws", Path: "/ws-x", Host: "ru.lab.firewalk.test", Route: nodespec.Direct, Clients: alice,
	})
	l.setBundle(t, ru4, spec)

	eventually(t, 10*time.Second, "the new inbound answers", exitsVia(1083))
	eventually(t, 10*time.Second, "de is blocked now", func() (bool, string) {
		got, err := getVia(1082, "http://web.lab.test/ip")
		if err == nil {
			return false, "answered " + got
		}
		return true, err.Error()
	})
	out, err := exec.Command("ip", "netns", "exec", "ru4", l.xray, "api", "inbounduser", "--server=127.0.0.1:10085", "-tag", "ru").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "bob") {
		t.Fatalf("bob is not a user of ru: %v %s", err, out)
	}
	if got := <-slow; got != "chunk0;chunk1;chunk2;chunk3;chunk4;chunk5;chunk6;chunk7;" {
		t.Fatalf("the download running during the change ended as %q", got)
	}
	if now := l.xrayPID("ru4"); now != pid {
		t.Fatalf("xray restarted: pid %d -> %d", pid, now)
	}

	spec.DirectAllow = []string{"10.0.0.0/8"}
	l.setBundle(t, ru4, spec)
	eventually(t, 10*time.Second, "a new default outbound restarts xray", func() (bool, string) {
		now := l.xrayPID("ru4")
		return now != 0 && now != pid, strconv.Itoa(now)
	})
	eventually(t, 10*time.Second, "ru answers after the restart", exitsVia(1081))
}
