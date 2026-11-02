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
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// A group is a named set of client emails: disabling it turns off every
// member's record on every node, and attaching it to an inbound creates the
// missing records, which reach the node's config.
func TestGroupsBulkOperations(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-g1", Host: "ru.lab.firewalk.test", Route: nodespec.Direct},
			{Tag: "vpn2", Port: 12055, Network: "ws", Path: "/ws-g2", Host: "ru.lab.firewalk.test", Route: nodespec.Direct},
		},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")

	call := func(method, via, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+ipOf(masterHosts[via])+":8080"+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	// two clients on vpn, then a group naming both
	for _, email := range []string{"ann", "ben"} {
		body := fmt.Sprintf(`{"node":"ru4","inbound":"vpn","email":%q,"uuid":%q,"enable":true}`, email, aliceID)
		eventually(t, 60*time.Second, "the spec replicates for "+email, func() (bool, string) {
			code, raw := call("PUT", "fi1", "/clients", body)
			if code == http.StatusCreated {
				return true, ""
			}
			return false, fmt.Sprintf("%d %s", code, raw)
		})
	}
	eventually(t, 60*time.Second, "the group is created on ru2", func() (bool, string) {
		code, raw := call("POST", "ru2", "/groups", `{"name":"vip","emails":["ann","ben"]}`)
		return code == http.StatusOK, fmt.Sprintf("%d %s", code, raw)
	})

	// the summary sees both members and their two records
	eventually(t, 60*time.Second, "the group summary counts both", func() (bool, string) {
		code, raw := call("GET", "de2", "/groups", "")
		if code != http.StatusOK {
			return false, string(raw)
		}
		var list []struct {
			Name    string `json:"name"`
			Records int    `json:"records"`
			Enabled int    `json:"enabled"`
		}
		if json.Unmarshal(raw, &list) != nil || len(list) != 1 {
			return false, string(raw)
		}
		return list[0].Records == 2 && list[0].Enabled == 2, string(raw)
	})

	// attach the group to the second inbound: both records are created
	eventually(t, 60*time.Second, "the group attaches to vpn2", func() (bool, string) {
		code, raw := call("POST", "fi1", "/groups/vip/attach", `{"node":"ru4","inbound":"vpn2"}`)
		if code != http.StatusOK || !strings.Contains(string(raw), `"created":2`) {
			return false, fmt.Sprintf("%d %s", code, raw)
		}
		return true, ""
	})
	eventually(t, 90*time.Second, "the attached clients reach the node", func() (bool, string) {
		b, err := os.ReadFile(filepath.Join(f.dir, "node-ru4", "data", "xray.json"))
		if err != nil {
			return false, err.Error()
		}
		// both emails appear on both inbounds: four client entries in total
		return strings.Count(string(b), `"email":"ann"`) == 2 && strings.Count(string(b), `"email":"ben"`) == 2, tailOf(string(b), 3)
	})

	// disabling the group turns every member off on every inbound
	if code, raw := call("POST", "de2", "/groups/vip/enable", `{"enable":false}`); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}
	eventually(t, 90*time.Second, "the group's clients leave the node", func() (bool, string) {
		b, err := os.ReadFile(filepath.Join(f.dir, "node-ru4", "data", "xray.json"))
		if err != nil {
			return false, err.Error()
		}
		return !strings.Contains(string(b), `"email":"ann"`) && !strings.Contains(string(b), `"email":"ben"`), tailOf(string(b), 3)
	})

	// detach removes the records from vpn2, leaving vpn's
	if code, raw := call("POST", "ru2", "/groups/vip/detach", `{"node":"ru4","inbound":"vpn2"}`); code != http.StatusOK ||
		!strings.Contains(string(raw), `"removed":2`) {
		t.Fatalf("detach: %d %s", code, raw)
	}
	eventually(t, 60*time.Second, "detaching removes only the second inbound's records", func() (bool, string) {
		code, raw := call("GET", "fi1", "/clients", "")
		if code != http.StatusOK {
			return false, string(raw)
		}
		var list []struct {
			Inbound string `json:"inbound"`
		}
		_ = json.Unmarshal(raw, &list)
		if len(list) != 2 {
			return false, string(raw)
		}
		for _, c := range list {
			if c.Inbound != "vpn" {
				return false, string(raw)
			}
		}
		return true, ""
	})
}
