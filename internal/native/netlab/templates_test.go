//go:build netlab

package netlab

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/templates"
)

// A template written through one master stamps the same spec onto two nodes,
// each with its own name and domain, and both nodes apply it. The template
// itself keeps its placeholders for the next use.
func TestTemplateStampsTwoNodes(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	f.startEnrolledNode(t, "ru4", "fi1")
	f.startEnrolledNode(t, "ru7", "ru2")

	tpl := nodespec.Spec{
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-tpl",
			Host: templates.NodeDomain, Route: nodespec.Direct,
		}},
		DirectAllow: []string{"10.0.0.0/16"},
	}
	body, _ := json.Marshal(map[string]any{"name": "entry", "remark": "RU entry", "spec": tpl})
	resp, err := http.Post("http://"+ipOf("fi1m")+":8080/templates", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %d", resp.StatusCode)
	}

	apply := `{"nodes":["ru4","ru7"],"domain":{"ru4":"ru.lab.firewalk.test","ru7":"ru7.lab.firewalk.test"}}`
	resp, err = http.Post("http://"+ipOf("ru2m")+":8080/templates/entry/apply", "application/json", strings.NewReader(apply))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"applied":true`) {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	for _, node := range []string{"ru4", "ru7"} {
		n := node
		eventually(t, 90*time.Second, n+" applies the template", func() (bool, string) {
			b, err := os.ReadFile(filepath.Join(f.dir, "node-"+n, "data", "xray.json"))
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(string(b), `"/ws-tpl"`), tailOf(string(b), 3)
		})
	}

	// each node's bundle carries its own host on the edge (the certificate
	// needs ACME, which this test does not run)
	for node, host := range map[string]string{"ru4": "ru.lab.firewalk.test", "ru7": "ru7.lab.firewalk.test"} {
		n, h := node, host
		eventually(t, 60*time.Second, n+" got its own host", func() (bool, string) {
			b, err := os.ReadFile(filepath.Join(f.dir, "node-"+n, "data", "state", "desired.json"))
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(string(b), h), tailOf(string(b), 2)
		})
	}

	resp, err = http.Get("http://" + ipOf("fi1m") + ":8080/templates")
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name string `json:"name"`
		Spec struct {
			Inbounds []struct {
				Host string `json:"host"`
			} `json:"inbounds"`
		} `json:"spec"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Spec.Inbounds[0].Host != templates.NodeDomain {
		t.Fatalf("the template kept the substituted host: %+v", list)
	}
}
