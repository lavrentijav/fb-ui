//go:build netlab

package netlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// A client that uses up its allowance is counted from the node's reports and
// disabled by the leader; its traffic total is readable over the API.
func TestUsageDisablesAnExhaustedClient(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-use", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "spot", ID: aliceID, LimitIP: 0}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")

	// a tiny allowance, so a single page load exhausts it
	body := fmt.Sprintf(`{"node":"ru4","inbound":"vpn","email":"spot","uuid":%q,"enable":true,"totalBytes":1024}`, aliceID)
	resp, err := http.Post("http://"+ipOf("fi1m")+":8080/clients", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	f.socksOn(t, "client", 1086, "/ws-use")
	// any request through the tunnel moves bytes the node reports
	go func() {
		for range 5 {
			_, _ = getViaFrom("client", 1086, "http://web.lab.test/ip")
			time.Sleep(300 * time.Millisecond)
		}
	}()

	eventually(t, 120*time.Second, "usage counts the client's traffic", func() (bool, string) {
		resp, err := http.Get("http://" + ipOf("ru2m") + ":8080/usage")
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var totals map[string]int64
		if json.Unmarshal(raw, &totals) != nil {
			return false, string(raw)
		}
		return totals["spot"] > 0, string(raw)
	})

	eventually(t, 120*time.Second, "the client is disabled at its allowance", func() (bool, string) {
		resp, err := http.Get("http://" + ipOf("fi1m") + ":8080/clients")
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var list []struct {
			Email  string `json:"email"`
			Enable bool   `json:"enable"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return false, string(raw)
		}
		if len(list) != 1 {
			return false, string(raw)
		}
		return !list[0].Enable, strings.TrimSpace(string(raw))
	})
}
