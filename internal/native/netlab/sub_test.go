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
	"strconv"
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

// subHeaders is the metadata a subscription response carries.
func (f *fleet) subHeaders(via, token string) (http.Header, error) {
	resp, err := http.Get("http://" + ipOf(masterHosts[via]) + ":8080/sub/" + token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sub: %d", resp.StatusCode)
	}
	return resp.Header, nil
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
		eventually(t, 40*time.Second, "the token renders links anywhere", func() (bool, string) {
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

	// the traffic the run just made shows up per server in the response, which
	// is the Flamix extension a client draws a bar from
	eventually(t, 90*time.Second, "the per-server traffic reaches the subscription", func() (bool, string) {
		h, err := f.subHeaders("fi1", token)
		if err != nil {
			return false, err.Error()
		}
		raw := h.Get("Server-Userinfo")
		if raw == "" {
			return false, "no Server-Userinfo yet"
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "base64:"))
		if err != nil {
			return false, err.Error()
		}
		var servers map[string]string
		if err := json.Unmarshal(b, &servers); err != nil {
			return false, string(b)
		}
		var got string
		for name, info := range servers {
			if strings.Contains(name, "ru4-vpn") {
				got = info
			}
		}
		if got == "" {
			return false, fmt.Sprintf("servers %v", servers)
		}
		var up, down int64
		for _, part := range strings.Split(got, ";") {
			part = strings.TrimSpace(part)
			if v, ok := strings.CutPrefix(part, "upload="); ok {
				up, _ = strconv.ParseInt(v, 10, 64)
			}
			if v, ok := strings.CutPrefix(part, "download="); ok {
				down, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		if up+down == 0 {
			return false, got
		}
		return true, got
	})
}

// A subscription response says more than the link list: quota, a name, an
// update interval and the transport parameters a modern client prefers.
func TestSubscriptionAdvertisesMetadata(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-meta", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID, LimitIP: 2}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")
	token := f.subToken(t, "fi1", "alice")

	h, err := f.subHeaders("fi1", token)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("Subscription-Userinfo"); !strings.HasPrefix(got, "upload=0; download=0; total=") {
		t.Fatalf("subscription-userinfo %q", got)
	}
	if got := h.Get("Profile-Update-Interval"); got != "12" {
		t.Fatalf("profile-update-interval %q", got)
	}
	title, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h.Get("Profile-Title"), "base64:"))
	if err != nil || string(title) != "alice" {
		t.Fatalf("profile-title %q %v", title, err)
	}
	if !strings.Contains(h.Get("Content-Disposition"), "filename*=UTF-8''alice") {
		t.Fatalf("content-disposition %q", h.Get("Content-Disposition"))
	}
	if got := h.Get("X-Hwid-Limit"); got != "2" {
		t.Fatalf("x-hwid-limit %q", got)
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
	for k, want := range map[string]string{
		"fp": "chrome", "headerType": "none", "allowInsecure": "0", "type": "ws",
		"path": "/ws-meta", "sni": "ru.lab.firewalk.test",
	} {
		if q.Get(k) != want {
			t.Fatalf("%s = %q, want %q in %s", k, q.Get(k), want, links[0])
		}
	}
	if u.Fragment != "ru4-vpn" {
		t.Fatalf("remark %q", u.Fragment)
	}

	// ?format=plain carries the same metadata as body lines, for a client or
	// CDN that strips headers
	resp, err := http.Get("http://" + ipOf(masterHosts["fi1"]) + ":8080/sub/" + token + "?format=plain")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, want := range []string{"#subscription-userinfo:", "#profile-title: base64:", "vless://"} {
		if !strings.Contains(body, want) {
			t.Fatalf("plain body lacks %q:\n%s", want, body)
		}
	}
}

// An advertised address replaces the node's own name in the subscription, so
// a client can be handed a CDN name or a spare IP without touching the spec.
func TestSubscriptionUsesAdvertisedAddresses(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-vpn", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "ru4")
	token := f.subToken(t, "fi1", "alice")

	// save the advertised address through the module's admin path on a master
	body := `{"node":"ru4","inbound":"vpn","address":"10.0.2.4","remark":"direct ip","enable":true}`
	resp, err := http.Post("http://"+ipOf("ru2m")+":8080/hosts", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save host: %d %s", resp.StatusCode, raw)
	}

	eventually(t, 30*time.Second, "the advertised address reaches the subscription", func() (bool, string) {
		links, err := f.subLinks("fi1", token)
		if err != nil || len(links) != 1 {
			return false, fmt.Sprintf("%v %v", links, err)
		}
		u, err := url.Parse(links[0])
		if err != nil {
			return false, err.Error()
		}
		return u.Hostname() == "10.0.2.4" && strings.HasSuffix(u.Fragment, "direct ip"), links[0]
	})
}
