//go:build netlab

package netlab

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

func (f *fleet) subToken(t *testing.T, via, email string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf(masterHosts[via])+":8080/sub/"+email, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("token: %v %+v", err, out)
	}
	return out.Token
}

func (f *fleet) subLinks(via, token string) ([]string, error) {
	resp, err := http.Get("http://" + ipOf(masterHosts[via]) + ":8080/sub/" + token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sub: %d %s", resp.StatusCode, body)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, fmt.Errorf("body is not base64: %q", body)
	}
	return strings.Split(string(raw), "\n"), nil
}

// A client's token renders to a link that actually connects through the
// node's edge, from a master that only knows the spec.
func TestSubscriptionLinkConnects(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "ru2", ru4)
	f.startNode(t, "ru4")

	token := f.subToken(t, "fi1", "alice")
	// a token minted through another master works once it replicates; two
	// masters minting at once can end up with two tokens for one email,
	// which is harmless because both render the same links
	other := f.subToken(t, "de2", "alice")
	for _, tok := range []string{token, other} {
		eventually(t, 20*time.Second, "the token renders links anywhere", func() (bool, string) {
			if _, err := f.subLinks("fi1", tok); err != nil {
				return false, err.Error()
			}
			if _, err := f.subLinks("ru2", tok); err != nil {
				return false, err.Error()
			}
			return true, ""
		})
	}
	links, err := f.subLinks("fi1", token)
	if err != nil || len(links) != 1 {
		t.Fatalf("links %v %v", links, err)
	}
	u, err := url.Parse(links[0])
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.User.Username() != aliceID || u.Hostname() != "ru.lab.firewalk.test" || q.Get("path") != "/ws-vpn" ||
		q.Get("type") != "ws" || q.Get("security") != "tls" || q.Get("sni") != "ru.lab.firewalk.test" {
		t.Fatalf("link %s", links[0])
	}
	resp, err := http.Get("http://" + ipOf("fi1m") + ":8080/sub/unknown-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown token answered %d", resp.StatusCode)
	}

	// the link as a client would use it
	cfg := fmt.Sprintf(`{"log":{"loglevel":"warning"},
	 "inbounds":[{"tag":"in","listen":"%s","port":1082,"protocol":"socks","settings":{"udp":false}}],
	 "outbounds":[{"tag":"out","protocol":"vless","settings":{"vnext":[{"address":"%s","port":%d,
	   "users":[{"id":"%s","encryption":"%s"}]}]},
	  "streamSettings":{"network":"%s","security":"%s",
	   "tlsSettings":{"serverName":"%s","alpn":["http/1.1"]},
	   "wsSettings":{"path":"%s","host":"%s"}}}]}`,
		ipOf("client"), u.Hostname(), 443, u.User.Username(), q.Get("encryption"),
		q.Get("type"), q.Get("security"), q.Get("sni"), q.Get("path"), q.Get("host"))
	file := filepath.Join(f.dir, "sub-client.json")
	if err := os.WriteFile(file, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	spawnCmd(t, "client", nil, f.xray, "run", "-c", file)
	waitTCP(t, ipOf("client")+":1082")

	eventually(t, 60*time.Second, "the link reaches the site through ru4", func() (bool, string) {
		got, err := getViaFrom("client", 1082, "http://web.lab.test/ip")
		if err != nil {
			return false, err.Error()
		}
		return got == "/ip "+ipOf("ru4"), got
	})
}
