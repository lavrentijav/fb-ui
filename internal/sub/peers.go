package sub

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/cluster"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// subMetaTTL bounds how stale an advertised fallback list can be. The peer
// health job runs every 30s, so a short cache costs nothing in freshness while
// keeping the per-request cost of a subscription fetch at zero queries.
const subMetaTTL = 10 * time.Second

// subMeta is everything the subscription responses need to know about the
// cluster: which sibling panels are reachable and how to sign the body.
type subMeta struct {
	fallbackEnable bool
	signEnable     bool
	emergencyURL   string
	signSeed       string
	fallbacks      []cluster.Endpoint // live peers, this panel excluded
	masters        []cluster.Endpoint // live peers, this panel included
	fetchedAt      time.Time
}

var (
	subMetaMu    sync.Mutex
	subMetaCache *subMeta
)

// loadSubMeta returns the cached cluster view, refreshing it when stale. It
// never returns nil: on a DB error the caller gets an empty view, which
// degrades to "no fallback advertised" rather than to a failed subscription.
func loadSubMeta() *subMeta {
	subMetaMu.Lock()
	defer subMetaMu.Unlock()
	if subMetaCache != nil && time.Since(subMetaCache.fetchedAt) < subMetaTTL {
		return subMetaCache
	}
	subMetaCache = buildSubMeta()
	return subMetaCache
}

// resetSubMetaCache drops the cached view so the next request rebuilds it.
func resetSubMetaCache() {
	subMetaMu.Lock()
	defer subMetaMu.Unlock()
	subMetaCache = nil
}

func buildSubMeta() *subMeta {
	meta := &subMeta{fetchedAt: time.Now()}
	// The sub server must never 500 on a subscription fetch, so an unopened DB
	// degrades to "no cluster metadata" instead of panicking on a nil handle.
	if database.GetDB() == nil {
		return meta
	}
	settingService := service.SettingService{}

	meta.fallbackEnable, _ = settingService.GetSubFallbackEnable()
	meta.signEnable, _ = settingService.GetSubSignEnable()
	meta.emergencyURL, _ = settingService.GetSubEmergencyUrl()

	if meta.signEnable {
		seed, _, err := settingService.SubSignKeypair()
		if err != nil {
			logger.Warning("sub: subscription signing key unavailable:", err)
		} else {
			meta.signSeed = seed
		}
	}

	if !meta.fallbackEnable {
		return meta
	}
	peerService := service.PeerService{}
	fallbacks, err := peerService.LiveEndpoints(false)
	if err != nil {
		logger.Warning("sub: load fallback peers failed:", err)
		return meta
	}
	masters, err := peerService.LiveEndpoints(true)
	if err != nil {
		logger.Warning("sub: load master peers failed:", err)
		return meta
	}
	meta.fallbacks, meta.masters = fallbacks, masters
	return meta
}

// applyClusterHeaders advertises the sibling panels a client can fall back to
// and signs the response body, so a client can tell a real subscription from a
// tampered one served by an interception proxy.
func applyClusterHeaders(c *gin.Context, body []byte) {
	meta := loadSubMeta()
	if meta.fallbackEnable {
		if domains := cluster.FallbackDomains(meta.fallbacks); domains != "" {
			c.Writer.Header().Set(cluster.HeaderFallbackDomains, domains)
		}
		if ips := cluster.FallbackIPs(meta.fallbacks); ips != "" {
			c.Writer.Header().Set(cluster.HeaderFallbackIPs, ips)
		}
	}
	if !meta.signEnable || meta.signSeed == "" {
		return
	}
	signature, err := cluster.Sign(meta.signSeed, body)
	if err != nil {
		logger.Warning("sub: signing the subscription body failed:", err)
		return
	}
	c.Writer.Header().Set(cluster.HeaderSignature, signature)
}

// identity publishes this panel's subscription signing key. Peers probe it to
// tell whether a configured fallback is reachable — and whether it is in fact
// this very panel, which must not advertise itself as its own fallback.
func (a *SUBController) identity(c *gin.Context) {
	_, public, err := (&service.SettingService{}).SubSignKeypair()
	if err != nil {
		logger.Warning("sub: loading the subscription signing key failed:", err)
		c.JSON(http.StatusInternalServerError, cluster.Identity{Alg: cluster.SignatureAlgorithm})
		return
	}
	setNoCacheHeaders(c)
	c.JSON(http.StatusOK, cluster.Identity{Alg: cluster.SignatureAlgorithm, PublicKey: public})
}
