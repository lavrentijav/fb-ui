//go:build netlab

package netlab

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// A client created through one master reaches the node's config and its
// subscription; disabling it removes it from both without losing the record.
func TestClientsReachNodesAndSubscriptions(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-cli", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct,
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")
	token := f.subToken(t, "fi1", "carol")

	post := func(via, body string) (int, []byte) {
		t.Helper()
		resp, err := http.Post("http://"+ipOf(masterHosts[via])+":8080/clients", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	create := fmt.Sprintf(`{"node":"ru4","inbound":"vpn","email":"carol","uuid":%q,"enable":true}`, aliceID)
	code, raw := post("ru2", create)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}

	eventually(t, 90*time.Second, "the client reaches the node's config", func() (bool, string) {
		b, err := os.ReadFile(filepath.Join(f.dir, "node-ru4", "data", "xray.json"))
		if err != nil {
			return false, err.Error()
		}
		return strings.Contains(string(b), `"email":"carol"`), tailOf(string(b), 3)
	})
	eventually(t, 60*time.Second, "the subscription lists the client", func() (bool, string) {
		links, err := f.subLinks("de2", token)
		if err != nil {
			return false, err.Error()
		}
		return len(links) == 1 && strings.Contains(links[0], aliceID), fmt.Sprint(links)
	})

	// disabled: gone from the config and the subscription, still listed
	disable := fmt.Sprintf(`{"node":"ru4","inbound":"vpn","email":"carol","uuid":%q,"enable":false}`, aliceID)
	if code, raw := post("fi1", disable); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}
	eventually(t, 90*time.Second, "a disabled client leaves the config", func() (bool, string) {
		b, err := os.ReadFile(filepath.Join(f.dir, "node-ru4", "data", "xray.json"))
		if err != nil {
			return false, err.Error()
		}
		return !strings.Contains(string(b), `"email":"carol"`), tailOf(string(b), 3)
	})
	eventually(t, 60*time.Second, "and leaves the subscription", func() (bool, string) {
		if _, err := f.subLinks("de2", token); err != nil {
			return true, err.Error()
		}
		return false, "still listed"
	})

	resp, err := http.Get("http://" + ipOf("ru2m") + ":8080/clients")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(raw, []byte(`"email":"carol"`)) || !bytes.Contains(raw, []byte(`"enable":false`)) {
		t.Fatalf("the record was lost: %s", raw)
	}
}
