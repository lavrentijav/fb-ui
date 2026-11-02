//go:build netlab

package netlab

import (
	"bytes"
	"encoding/base64"
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

// A subscription has a login and a generated password: a browser opening its
// URL gets the page, a client gets the links, and the subscriber can manage
// their virtual network after logging in.
func TestSubscriptionPageAndAccount(t *testing.T) {
	uiDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(uiDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := `<!doctype html><html><head><title>Subscription</title>
<script type="module" src="/assets/subpage-test.js"></script></head>
<body><div id="app"></div></body></html>`
	if err := os.WriteFile(filepath.Join(uiDir, "subpage.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "assets", "subpage-test.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	f.startMasters(t, "NETLAB_UIDIR="+uiDir)
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{{
			Tag: "vpn", Port: 12054, Network: "ws", Path: "/ws-acc", Host: "ru.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "fi1", ru4)

	call := func(method, via, path, body string, cookie *http.Cookie, accept string) (int, []byte, []*http.Cookie) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+ipOf(masterHosts[via])+":8080"+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw, resp.Cookies()
	}

	// create the subscription with the login the operator will hand out
	var token string
	eventually(t, 60*time.Second, "the subscription is created", func() (bool, string) {
		code, raw, _ := call("PUT", "fi1", "/sub/alice", `{"user":"alice01","password":"supersecret"}`, nil, "")
		if code != http.StatusOK {
			return false, fmt.Sprintf("%d %s", code, raw)
		}
		var out struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(raw, &out) != nil || out.Token == "" {
			return false, string(raw)
		}
		token = out.Token
		return true, ""
	})

	// a browser gets the page with the subscription baked in
	code, raw, _ := call("GET", "fi1", "/sub/"+token, "", nil, "text/html,application/xhtml+xml")
	if code != http.StatusOK || !strings.Contains(string(raw), "__SUB_PAGE_DATA__") ||
		!strings.Contains(string(raw), "alice01") || !strings.Contains(string(raw), `/panel/assets/`) {
		t.Fatalf("page: %d %.300s", code, raw)
	}
	// a client gets the links
	code, raw, _ = call("GET", "fi1", "/sub/"+token, "", nil, "*/*")
	if code != http.StatusOK {
		t.Fatalf("links: %d %.120s", code, raw)
	}
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err != nil ||
		!strings.Contains(string(decoded), aliceID) {
		t.Fatalf("links body %.120s", raw)
	}
	// its assets load from the panel's static mount
	if code, raw, _ := call("GET", "de2", "/panel/assets/subpage-test.js", "", nil, ""); code != http.StatusOK ||
		!strings.Contains(string(raw), "console.log") {
		t.Fatalf("asset: %d %.80s", code, raw)
	}

	// the subscriber logs in and sees their own links (the record reaches
	// this master by replication)
	var sess *http.Cookie
	eventually(t, 60*time.Second, "the subscription login works on another master", func() (bool, string) {
		code, raw, cookies := call("POST", "de2", "/api/v1/sub/login", `{"user":"alice01","password":"supersecret"}`, nil, "")
		if code == http.StatusOK && len(cookies) > 0 {
			sess = cookies[0]
			return true, ""
		}
		return false, fmt.Sprintf("%d %s", code, raw)
	})
	if !sess.HttpOnly {
		t.Fatalf("cookie %+v", sess)
	}
	if code, raw, _ := call("GET", "de2", "/api/v1/sub/me", "", sess, ""); code != http.StatusOK ||
		!strings.Contains(string(raw), `"user":"alice01"`) || !strings.Contains(string(raw), aliceID) {
		t.Fatalf("me: %d %.200s", code, raw)
	}
	// a wrong password is refused
	if code, _, _ := call("POST", "de2", "/api/v1/sub/login", `{"user":"alice01","password":"nope"}`, nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d", code)
	}

	// the subscriber creates a network and invites a friend
	if code, raw, _ := call("POST", "de2", "/api/v1/sub/vnet", `{"title":"friends"}`, sess, ""); code != http.StatusOK {
		t.Fatalf("create vnet: %d %s", code, raw)
	}
	code, raw, _ = call("GET", "de2", "/api/v1/sub/me", "", sess, "")
	if code != http.StatusOK || !strings.Contains(string(raw), `"network"`) {
		t.Fatalf("me after vnet: %d %.300s", code, raw)
	}
	code, raw, _ = call("POST", "de2", "/api/v1/sub/vnet/invites", `{"ttl":"1h","uses":1}`, sess, "")
	if code != http.StatusOK {
		t.Fatalf("invite: %d %s", code, raw)
	}
	var inv struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &inv)
	if inv.Code == "" {
		t.Fatalf("invite %s", raw)
	}

	// a second subscriber joins with the code
	var bobToken string
	eventually(t, 60*time.Second, "the second subscription is created", func() (bool, string) {
		code, raw, _ := call("PUT", "fi1", "/sub/bob", `{"user":"bob01","password":"othersecret"}`, nil, "")
		if code != http.StatusOK {
			return false, fmt.Sprintf("%d %s", code, raw)
		}
		var out struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(raw, &out)
		bobToken = out.Token
		return bobToken != "", string(raw)
	})
	_ = bobToken
	var bob *http.Cookie
	eventually(t, 60*time.Second, "the second subscriber logs in", func() (bool, string) {
		code, raw, cookies := call("POST", "fi1", "/api/v1/sub/login", `{"user":"bob01","password":"othersecret"}`, nil, "")
		if code == http.StatusOK && len(cookies) > 0 {
			bob = cookies[0]
			return true, ""
		}
		return false, fmt.Sprintf("%d %s", code, raw)
	})
	if code, raw, _ := call("POST", "ru2", "/api/v1/sub/vnet/join", fmt.Sprintf(`{"code":%q}`, inv.Code), bob, ""); code != http.StatusOK {
		t.Fatalf("join: %d %s", code, raw)
	}
	code, raw, _ = call("GET", "fi1", "/api/v1/sub/me", "", bob, "")
	if code != http.StatusOK || !strings.Contains(string(raw), `"id":"alice"`) {
		t.Fatalf("bob's members: %d %.400s", code, raw)
	}

	// the operator lists subscriptions with their login and links
	eventually(t, 60*time.Second, "both subscriptions are listed", func() (bool, string) {
		code, raw, _ := call("GET", "fi1", "/sub", "", nil, "")
		if code != http.StatusOK {
			return false, fmt.Sprintf("%d %s", code, raw)
		}
		var list []struct {
			Email string `json:"email"`
			User  string `json:"user"`
			Links int    `json:"links"`
		}
		if json.Unmarshal(raw, &list) != nil || len(list) != 2 {
			return false, string(raw)
		}
		for _, it := range list {
			if it.User == "" {
				return false, string(raw)
			}
			if it.Email == "alice" && it.Links == 0 {
				return false, string(raw)
			}
		}
		return list[0].Email == "alice" && list[1].Email == "bob", string(raw)
	})

	// the operator can switch a link list off without touching the login
	code, raw, _ = call("GET", "fi1", "/sub", "", nil, "")
	var listed []struct {
		Token string `json:"token"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("list: %s", raw)
	}
	aliceToken := ""
	for _, it := range listed {
		if it.Email == "alice" {
			aliceToken = it.Token
		}
	}
	if code, raw, _ := call("POST", "fi1", "/sub/"+aliceToken+"/enable", `{"enable":false}`, nil, ""); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}
	if code, _, _ := call("GET", "fi1", "/sub/"+aliceToken, "", nil, "*/*"); code != http.StatusNotFound {
		t.Fatalf("a disabled subscription answered %d", code)
	}
	if code, raw, _ := call("POST", "fi1", "/sub/"+aliceToken+"/enable", `{"enable":true}`, nil, ""); code != http.StatusOK {
		t.Fatalf("enable: %d %s", code, raw)
	}

	// the operator sees the network both subscribers joined
	eventually(t, 60*time.Second, "the network is listed with both members", func() (bool, string) {
		code, raw, _ := call("GET", "de2", "/vnet/networks", "", nil, "")
		if code != http.StatusOK {
			return false, fmt.Sprintf("%d %s", code, raw)
		}
		var nets []struct {
			Members []struct {
				User string `json:"user"`
				IP   string `json:"ip"`
			} `json:"members"`
		}
		if json.Unmarshal(raw, &nets) != nil || len(nets) != 1 || len(nets[0].Members) != 2 {
			return false, string(raw)
		}
		return nets[0].Members[0].IP != "" && nets[0].Members[1].IP != "", string(raw)
	})
}
