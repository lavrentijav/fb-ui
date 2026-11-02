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

// An inbound added through the API reaches the node's config; a port change
// moves it there; deleting it removes it. A rule that names the inbound stops
// the delete before anything is written.
func TestInboundEditingReachesTheNode(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-a", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct,
		}},
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
	config := func(want bool, needle string) func() (bool, string) {
		return func() (bool, string) {
			b, err := os.ReadFile(filepath.Join(f.dir, "node-ru4", "data", "xray.json"))
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(string(b), needle) == want, tailOf(string(b), 3)
		}
	}

	add := `{"tag":"de","port":12060,"network":"ws","path":"/ws-de","host":"ru.lab.firewalk.test","route":"direct"}`
	eventually(t, 60*time.Second, "the spec replicates to ru2", func() (bool, string) {
		code, raw := call("POST", "ru2", "/inbounds/ru4", add)
		if code == http.StatusCreated {
			return true, ""
		}
		return false, fmt.Sprintf("%d %s", code, raw)
	})
	eventually(t, 90*time.Second, "the new inbound reaches the node", config(true, `"/ws-de"`))

	update := `{"tag":"de","port":12061,"network":"ws","path":"/ws-de","host":"ru.lab.firewalk.test","route":"direct"}`
	if code, raw := call("PUT", "fi1", "/inbounds/ru4/de", update); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, raw)
	}
	eventually(t, 90*time.Second, "the port change reaches the node", config(true, `"port":12061`))

	// a rule that names "de" blocks the delete
	spec, _ := readSpec(t, f, "ru4")
	spec.Rules = append(spec.Rules, nodespec.Rule{Name: "pin", Inbounds: []string{"de"}, Domains: []string{"x.example"}, Route: nodespec.Direct})
	if code, raw := putSpecRaw(t, f, "de2", spec); code != http.StatusOK {
		t.Fatalf("spec with a rule: %d %s", code, raw)
	}
	eventually(t, 60*time.Second, "the rule reaches the node", config(true, `"x.example"`))
	if code, raw := call("DELETE", "ru2", "/inbounds/ru4/de", ""); code != http.StatusBadRequest ||
		!strings.Contains(string(raw), "still names") {
		t.Fatalf("delete under a rule: %d %s", code, raw)
	}

	// without the rule the delete goes through
	spec.Rules = nil
	if code, raw := putSpecRaw(t, f, "fi1", spec); code != http.StatusOK {
		t.Fatalf("spec without a rule: %d %s", code, raw)
	}
	if code, raw := call("DELETE", "de2", "/inbounds/ru4/de", ""); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, raw)
	}
	eventually(t, 90*time.Second, "the deleted inbound leaves the node", config(false, `"/ws-de"`))
}

func readSpec(t *testing.T, f *fleet, node string) (nodespec.Spec, bool) {
	t.Helper()
	resp, err := http.Get("http://" + ipOf("de2m") + ":8080/inbounds/" + node)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var inbounds []nodespec.Inbound
	if json.NewDecoder(resp.Body).Decode(&inbounds) != nil {
		return nodespec.Spec{}, false
	}
	// the rules are not in the inbound list; re-read the spec through the file
	return nodespec.Spec{Node: node, Inbounds: inbounds, DirectAllow: []string{"10.0.0.0/16"}}, true
}

func putSpecRaw(t *testing.T, f *fleet, via string, spec nodespec.Spec) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(spec)
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf(masterHosts[via])+":8080/nodes/"+spec.Node+"/spec", bytes.NewReader(raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
