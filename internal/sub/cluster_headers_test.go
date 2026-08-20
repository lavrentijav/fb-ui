package sub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/cluster"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setSubSetting(t *testing.T, key, value string) {
	t.Helper()
	if err := database.GetDB().Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("seed setting %s: %v", key, err)
	}
	resetSubMetaCache()
}

// clearPeers empties the table so a test can assert over the whole live set:
// under XUI_DB_TYPE=postgres every test in the package shares one database.
func clearPeers(t *testing.T) {
	t.Helper()
	if err := database.GetDB().Where("1 = 1").Delete(&model.MasterPeer{}).Error; err != nil {
		t.Fatalf("clear peers: %v", err)
	}
	resetSubMetaCache()
}

func seedPeer(t *testing.T, p *model.MasterPeer) *model.MasterPeer {
	t.Helper()
	p.Name = t.Name() + "/" + p.Name
	// Create refreshes Enable from the DB default when it was false, so the
	// intent has to be captured before the insert.
	wantEnabled := p.Enable
	if err := database.GetDB().Create(p).Error; err != nil {
		t.Fatalf("seed peer %s: %v", p.Name, err)
	}
	if !wantEnabled {
		if err := database.GetDB().Model(model.MasterPeer{}).Where("id = ?", p.Id).Update("enable", false).Error; err != nil {
			t.Fatalf("disable peer %s: %v", p.Name, err)
		}
		p.Enable = false
	}
	resetSubMetaCache()
	return p
}

func getSub(t *testing.T, router *gin.Engine, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "sub1.example.com"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Peer rows exist for the panel's own bookkeeping; nothing may reach clients
// until fallback advertising is explicitly turned on.
func TestFallbackHeadersAbsentUntilEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seedSubDB(t)
	clearPeers(t)
	seedSubInbound(t, "s1", "fb-off", 4601, 1, `{"network":"tcp","security":"none"}`)
	seedPeer(t, &model.MasterPeer{Name: "p2", Scheme: "https", Domain: "sub2.example.com", Port: 2096, SubPath: "/sub/", Enable: true, Status: "online"})

	w := getSub(t, newSubscriptionTestRouter(subscriptionTestRouterConfig{}), "/sub/s1")

	if got := w.Header().Get(cluster.HeaderFallbackDomains); got != "" {
		t.Fatalf("fallback domains leaked while disabled: %q", got)
	}
	if got := w.Header().Get(cluster.HeaderSignature); got != "" {
		t.Fatalf("signature emitted while disabled: %q", got)
	}
}

func TestFallbackHeadersAdvertiseReachablePeersOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seedSubDB(t)
	clearPeers(t)
	seedSubInbound(t, "s1", "fb-on", 4602, 1, `{"network":"tcp","security":"none"}`)
	setSubSetting(t, "subFallbackEnable", "true")

	seedPeer(t, &model.MasterPeer{Name: "live", Scheme: "https", Domain: "sub2.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.2"}, Enable: true, Status: "online"})
	seedPeer(t, &model.MasterPeer{Name: "down", Scheme: "https", Domain: "sub3.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.3"}, Enable: true, Status: "offline"})
	seedPeer(t, &model.MasterPeer{Name: "self", Scheme: "https", Domain: "sub1.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.1"}, Enable: true, Status: "online", IsSelf: true})
	seedPeer(t, &model.MasterPeer{Name: "paused", Scheme: "https", Domain: "sub4.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.4"}, Enable: false, Status: "online"})

	w := getSub(t, newSubscriptionTestRouter(subscriptionTestRouterConfig{}), "/sub/s1")

	if got := w.Header().Get(cluster.HeaderFallbackDomains); got != "sub2.example.com" {
		t.Fatalf("fallback domains = %q, want only the reachable non-self peer", got)
	}
	if got := w.Header().Get(cluster.HeaderFallbackIPs); got != "185.51.100.2" {
		t.Fatalf("fallback IPs = %q, want only the reachable non-self peer", got)
	}
}

func TestSubscriptionSignatureCoversTheServedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seedSubDB(t)
	seedSubInbound(t, "s1", "sig", 4603, 1, `{"network":"tcp","security":"none"}`)
	setSubSetting(t, "subSignEnable", "true")
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})

	keyResp := getSub(t, router, "/sub/pubkey")
	if keyResp.Code != http.StatusOK {
		t.Fatalf("pubkey endpoint status = %d", keyResp.Code)
	}
	var identity cluster.Identity
	if err := json.Unmarshal(keyResp.Body.Bytes(), &identity); err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	if identity.Alg != cluster.SignatureAlgorithm || identity.PublicKey == "" {
		t.Fatalf("identity = %+v, want an ed25519 key", identity)
	}

	for _, path := range []string{"/sub/s1", "/json/s1", "/clash/s1"} {
		t.Run(path, func(t *testing.T) {
			w := getSub(t, router, path)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			signature := w.Header().Get(cluster.HeaderSignature)
			if signature == "" {
				t.Fatal("no signature header")
			}
			if err := cluster.Verify(identity.PublicKey, signature, w.Body.Bytes()); err != nil {
				t.Fatalf("signature does not cover the served body: %v", err)
			}
			tampered := append([]byte(nil), w.Body.Bytes()...)
			tampered[0] ^= 0x01
			if err := cluster.Verify(identity.PublicKey, signature, tampered); err == nil {
				t.Fatal("signature verified a tampered body")
			}
		})
	}
}

// The whole point of a separate ?format=meta document: turning the cluster
// features on must not move a single byte of what existing clients receive.
func TestClusterFeaturesDoNotChangeSubscriptionBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seedSubDB(t)
	clearPeers(t)
	seedSubInbound(t, "s1", "compat", 4604, 1, `{"network":"tcp","security":"none"}`)
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})

	paths := []string{"/sub/s1", "/json/s1", "/clash/s1"}
	before := make(map[string]string, len(paths))
	for _, p := range paths {
		before[p] = getSub(t, router, p).Body.String()
	}

	setSubSetting(t, "subFallbackEnable", "true")
	setSubSetting(t, "subSignEnable", "true")
	seedPeer(t, &model.MasterPeer{Name: "live", Scheme: "https", Domain: "sub2.example.com", Port: 2096, SubPath: "/sub/", Enable: true, Status: "online"})

	for _, p := range paths {
		if got := getSub(t, router, p).Body.String(); got != before[p] {
			t.Fatalf("%s body changed after enabling cluster features:\nbefore: %q\nafter:  %q", p, before[p], got)
		}
	}
}

func TestSubMetaFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seedSubDB(t)
	clearPeers(t)
	seedSubInbound(t, "s1", "meta", 4605, 1, `{"network":"tcp","security":"none"}`)
	setSubSetting(t, "subFallbackEnable", "true")
	setSubSetting(t, "subEmergencyUrl", "https://example.invalid/emergency.json")
	seedPeer(t, &model.MasterPeer{Name: "self", Scheme: "https", Domain: "sub1.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.1"}, Enable: true, Status: "online", IsSelf: true})
	seedPeer(t, &model.MasterPeer{Name: "live", Scheme: "https", Domain: "sub2.example.com", Port: 2096, SubPath: "/sub/", Ips: []string{"185.51.100.2"}, Enable: true, Status: "online"})
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})

	w := getSub(t, router, "/sub/s1?format=meta")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var payload SubMetaPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode meta payload: %v", err)
	}
	if payload.Version != SubMetaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, SubMetaVersion)
	}
	if payload.UpdatedAt <= 0 {
		t.Fatalf("updated_at = %d, want a unix timestamp", payload.UpdatedAt)
	}
	if payload.Meta.EmergencyURL != "https://example.invalid/emergency.json" {
		t.Fatalf("emergency_url = %q", payload.Meta.EmergencyURL)
	}
	// Unlike the fallback headers, the master list is the full cluster view and
	// keeps the panel serving the request.
	domains := make([]string, 0, len(payload.Meta.Masters))
	for _, m := range payload.Meta.Masters {
		domains = append(domains, m.Domain)
	}
	if strings.Join(domains, ",") != "sub1.example.com,sub2.example.com" {
		t.Fatalf("masters = %v, want both panels including this one", domains)
	}

	var configs []map[string]any
	if err := json.Unmarshal(payload.Outbounds, &configs); err != nil {
		t.Fatalf("outbounds is not the JSON subscription array: %v", err)
	}
	if len(configs) == 0 {
		t.Fatal("outbounds is empty")
	}
	if w.Header().Get(cluster.HeaderFallbackDomains) != "sub2.example.com" {
		t.Fatalf("meta response missing fallback headers: %#v", w.Header())
	}
}
