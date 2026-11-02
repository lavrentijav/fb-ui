//go:build netlab

package netlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// enrollToken mints a one-time token through a master's admin API.
func (f *fleet) enrollToken(t *testing.T, via, node string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"node": node, "ttl": "1h"})
	resp, err := http.Post("http://"+ipOf(masterHosts[via])+":8080/enroll/tokens", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("token: %v %+v", err, out)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %d", resp.StatusCode)
	}
	return out.Token
}

// startEnrolledNode enrols node through via, writes the master's answer as
// its identity and config, and runs the real fbnode.
func (f *fleet) startEnrolledNode(t *testing.T, node, via string) {
	t.Helper()
	token := f.enrollToken(t, via, node)
	res, err := enroll.Exchange(context.Background(), "http://"+ipOf(masterHosts[via])+":8080", token, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Node != node {
		t.Fatalf("enrolled %q", res.Node)
	}
	dir := filepath.Join(f.dir, "node-"+node)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"ca.pem": res.CA, "cert.pem": res.Cert, "key.pem": res.Key} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	masters := map[string]string{}
	for m := range masterHosts {
		masters[m] = "tls://" + publicName(m) + ":443"
	}
	writeJSON(t, filepath.Join(dir, "config.json"), map[string]any{
		"node": node, "masters": masters, "ca": filepath.Join(dir, "ca.pem"),
		"cert": filepath.Join(dir, "cert.pem"), "key": filepath.Join(dir, "key.pem"),
		"dataDir": filepath.Join(dir, "data"), "xray": map[string]string{"bin": f.xray},
		"reportEvery": "1s", "pullEvery": "1s", "banEvery": "1s", "heartbeat": "2s",
	})
	cmd := spawnCmd(t, node, nil, os.Getenv("NETLAB_FBNODE"), "-c", filepath.Join(dir, "config.json"))
	if f.nodes == nil {
		f.nodes = map[string]interface{ Kill() error }{}
	}
	f.nodes[node] = cmd.Process
}

// A node enrols with a one-time token, gets a certificate whose CN the bus
// accepts, applies its spec and serves traffic; the token is then spent.
func TestNodeEnrollsItself(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t)

	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-enroll", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startEnrolledNode(t, "ru4", "ru2")

	f.client(t, map[int]string{1084: "/ws-enroll"})
	eventually(t, 60*time.Second, "the enrolled node serves traffic", func() (bool, string) {
		got, err := getVia(1084, "http://web.lab.test/ip")
		if err != nil {
			return false, err.Error()
		}
		return got == "/ip "+ipOf("ru4"), got
	})

	// traffic makes the node report; the report proves the certificate is
	// accepted for publishing on every master that mirrors it
	eventually(t, 60*time.Second, "the enrolled node reports to a master", func() (bool, string) {
		var sum uint64
		last := ""
		for _, m := range masterHosts {
			resp, err := http.Get("http://" + ipOf(m) + ":8080/reports")
			if err != nil {
				continue
			}
			var heads map[string]uint64
			_ = json.NewDecoder(resp.Body).Decode(&heads)
			resp.Body.Close()
			for name, n := range heads {
				sum += n
				last = name
			}
		}
		return sum > 0, fmt.Sprintf("no reports yet (%s)", last)
	})

	redeem := func(via, token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+ipOf(masterHosts[via])+":8080/enroll",
			bytes.NewReader([]byte(`{"node":"ru4"}`)))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := redeem("ru2", ""); code != http.StatusUnauthorized {
		t.Fatalf("a tokenless redeem answered %d", code)
	}
	// a token works once: redeemed through ru2, refused by fi1 after it replicates
	token := f.enrollToken(t, "fi1", "ru4")
	if code := redeem("ru2", token); code != http.StatusOK {
		t.Fatalf("a fresh token answered %d", code)
	}
	eventually(t, 20*time.Second, "the spent token is refused everywhere", func() (bool, string) {
		if code := redeem("fi1", token); code != http.StatusUnauthorized {
			return false, fmt.Sprintf("answered %d", code)
		}
		return true, ""
	})
}
