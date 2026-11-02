//go:build netlab

package netlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func (f *fleet) user(t *testing.T, id string) vnet.User {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf("fi1m")+":8080/users/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u vnet.User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil || u.IP == "" {
		t.Fatalf("user %s: %v %+v", id, err, u)
	}
	return u
}

func (f *fleet) api(t *testing.T, via, token, method, path, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+ipOf(masterHosts[via])+":8080/api/v1"+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return out
}

// member runs a member's device: the game on its own loopback, its Xray
// tunnel to entry, and a local port that reaches peerIP:25565 through it.
func (f *fleet) member(t *testing.T, host string, u vnet.User, entry, peerIP string) interface{ Kill() error } {
	t.Helper()
	spawn(t, host, "game", "NETLAB_ADDR=127.0.0.1:25565")
	cfg, err := vnet.ClientConfig(u, vnet.ClientEntry{
		Address: entry, Port: 443, SNI: entry, Host: entry, Network: "ws", Path: "/ws-vpn",
	}, []map[string]any{{
		"tag": "to-peer", "listen": ipOf(host), "port": 30000, "protocol": "tunnel",
		"settings": map[string]any{"address": peerIP, "port": 25565, "network": "tcp,udp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.dir, host+".json")
	os.WriteFile(file, cfg, 0o600)
	return spawnCmd(t, host, nil, f.xray, "run", "-c", file).Process
}

func vnetNode(name, host string) nodespec.Spec {
	return nodespec.Spec{
		Node: name,
		Inbounds: []nodespec.Inbound{
			{Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: host, Route: nodespec.Direct, VNet: true},
			{Tag: "cascade", Port: 12060, Network: "ws", Path: "/ws-net", Host: host, Route: nodespec.Direct,
				Clients: []nodespec.Client{{Email: vnet.CascadeUser, ID: serversID}}},
		},
		VNetHop:     &nodespec.Hop{Address: host, Port: 443, ID: serversID, Network: "ws", Path: "/ws-net", SNI: host, Host: host},
		DirectAllow: []string{"10.0.0.0/16"},
	}
}

// Spec tests 1, 2, 4, 5: alice on ru4 and bob on de2 play over the network
// both ways, TCP and UDP; carol outside it cannot; bob moves to ru4 and is
// back; alice's node dies and she is back once she reconnects elsewhere;
// bob removed loses alice.
func TestMembersOnDifferentNodes(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4, de2 := vnetNode("ru4", "ru4.lab.firewalk.test"), vnetNode("de2", "de2.lab.firewalk.test")
	ru4.Certs = []nodespec.Cert{f.cert(t, "ru4.lab.firewalk.test")}
	de2.Certs = []nodespec.Cert{f.cert(t, "de2.lab.firewalk.test")}
	alice, bob, carol := f.user(t, "alice"), f.user(t, "bob"), f.user(t, "carol")
	f.putSpec(t, "fi1", ru4)
	f.putSpec(t, "ru2", de2)
	f.startNode(t, "ru4")
	f.startNode(t, "de2")

	f.api(t, "fi1", alice.Token, "POST", "/network", `{"title":"игра"}`)
	inv := f.api(t, "de2", alice.Token, "POST", "/network/invites", `{"ttl":"1h","uses":1}`)
	f.api(t, "ru2", bob.Token, "POST", "/network/join", fmt.Sprintf(`{"code":%q}`, inv["code"]))

	aliceDev := f.member(t, "alice", alice, "ru4.lab.firewalk.test", bob.IP)
	bobDev := f.member(t, "bob", bob, "de2.lab.firewalk.test", alice.IP)
	f.member(t, "client", carol, "ru4.lab.firewalk.test", bob.IP)

	reaches := func(host, what string) {
		t.Helper()
		eventually(t, 60*time.Second, what, func() (bool, string) {
			got, err := tcpAsk(ipOf(host)+":30000", "hi")
			if err != nil {
				return false, err.Error()
			}
			return got == "tcp from player: hi", got
		})
	}
	reaches("alice", "alice (ru4) reaches bob (de2) over TCP")
	reaches("bob", "bob (de2) reaches alice (ru4) over TCP")
	if got, err := udpAsk(ipOf("alice")+":30000", "u"); err != nil || got != "udp from player: u" {
		t.Fatalf("UDP alice → bob: %q %v", got, err)
	}
	if got, err := tcpAsk(ipOf("client")+":30000", "hi"); err == nil {
		t.Fatalf("carol, outside the network, reached bob: %q", got)
	}

	// test 2: bob moves to ru4
	_ = bobDev.Kill()
	bobDev = f.member(t, "bob", bob, "ru4.lab.firewalk.test", alice.IP)
	reaches("alice", "bob is reachable again after moving to ru4")

	// test 5: alice's node (ru4) dies; alice reconnects to de2
	_ = f.nodes["ru4"].Kill()
	_ = aliceDev.Kill()
	aliceDev = f.member(t, "alice", alice, "de2.lab.firewalk.test", bob.IP)
	_ = bobDev.Kill()
	bobDev = f.member(t, "bob", bob, "de2.lab.firewalk.test", alice.IP)
	reaches("alice", "alice is back through de2 after ru4 died")

	// test 4: bob removed loses alice
	f.api(t, "fi1", alice.Token, "DELETE", "/network/members/bob", "")
	eventually(t, 30*time.Second, "bob lost the network", func() (bool, string) {
		got, err := tcpAsk(ipOf("bob")+":30000", "hi")
		if err != nil {
			return true, err.Error()
		}
		return false, got
	})
	_ = aliceDev
}
