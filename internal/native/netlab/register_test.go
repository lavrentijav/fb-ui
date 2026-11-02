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
)

func (f *fleet) register(t *testing.T, via string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post("http://"+ipOf(masterHosts[via])+":8080/provision/register", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		out = map[string]any{"text": string(b)}
	}
	return resp.StatusCode, out
}

// Register builds both specs from a role and a name: the exit first, then the
// entry that cascades to it. The running nodes pick the specs up and the
// entry's Xray grows the proxy_de cascade; a second register refuses unless
// overwrite is set.
func TestRegisterBuildsTheFleet(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	f.startEnrolledNode(t, "de2", "fi1")
	f.startEnrolledNode(t, "ru4", "ru2")

	code, out := f.register(t, "fi1", map[string]any{
		"node": "de2", "role": "exit", "host": "de2.lab.firewalk.test", "port": 12060, "path": "/ws-net",
	})
	if code != http.StatusOK {
		t.Fatalf("exit: %d %v", code, out)
	}
	code, out = f.register(t, "ru2", map[string]any{
		"node": "ru4", "role": "entry", "host": "ru.lab.firewalk.test",
		"port": 12054, "path": "/ws-vpn", "cascadeTo": "de2", "country": "de", "vnet": true,
	})
	if code != http.StatusOK {
		raw, _ := os.ReadFile(filepath.Join(f.dir, "node-ru4", "config.json"))
		t.Fatalf("entry: %d %v (ru4 config %s)", code, out, raw)
	}
	if !strings.Contains(out["summary"].(string), "exit:de") {
		t.Fatalf("summary %v", out["summary"])
	}
	if code, _ := f.register(t, "de2", map[string]any{
		"node": "de2", "role": "exit", "host": "de2.lab.firewalk.test",
	}); code != http.StatusConflict {
		t.Fatalf("a second register: %d", code)
	}
	if code, _ := f.register(t, "de2", map[string]any{
		"node": "ru4", "role": "entry", "host": "ru.lab.firewalk.test",
		"cascadeTo": "de2", "overwrite": true,
	}); code != http.StatusOK {
		t.Fatalf("overwrite: %d", code)
	}

	applied := func(node, want string) func() (bool, string) {
		return func() (bool, string) {
			b, err := os.ReadFile(filepath.Join(f.dir, "node-"+node, "data", "xray.json"))
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(string(b), want), tailOf(string(b), 3)
		}
	}
	eventually(t, 90*time.Second, "the exit serves its cascade", applied("de2", `"cascade"`))
	eventually(t, 90*time.Second, "the entry cascades to de2", applied("ru4", `"proxy_de"`))
	eventually(t, 90*time.Second, "the entry's hop points at de2", applied("ru4", "de2.lab.firewalk.test"))

	// the fleet view: both registered nodes are online and have a spec
	eventually(t, 60*time.Second, "GET /nodes/state shows both nodes", func() (bool, string) {
		resp, err := http.Get("http://" + ipOf("fi1m") + ":8080/nodes/state")
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		var states []struct {
			Node    string `json:"node"`
			HasSpec bool   `json:"hasSpec"`
			Online  bool   `json:"online"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&states); err != nil {
			return false, err.Error()
		}
		online := map[string]bool{}
		for _, s := range states {
			online[s.Node] = s.HasSpec && s.Online
		}
		return online["ru4"] && online["de2"], fmt.Sprint(states)
	})
}
