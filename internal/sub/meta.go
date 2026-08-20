package sub

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// SubMetaVersion is the schema version of the ?format=meta document. Bump it
// only for a breaking change; additive fields keep the same version.
const SubMetaVersion = 1

// SubMetaPayload is the cluster-aware subscription document served at
// ?format=meta. It is a separate format on purpose: the raw and JSON
// subscriptions must stay byte-identical for the client apps already parsing
// them, which cannot tolerate extra top-level keys.
type SubMetaPayload struct {
	Version   int             `json:"version"`
	UpdatedAt int64           `json:"updated_at"`
	Meta      SubMetaInfo     `json:"meta"`
	Outbounds json.RawMessage `json:"outbounds"`
}

type SubMetaInfo struct {
	Masters      []SubMetaMaster `json:"masters"`
	EmergencyURL string          `json:"emergency_url,omitempty"`
}

type SubMetaMaster struct {
	Domain string   `json:"domain"`
	Ips    []string `json:"ips,omitempty"`
}

// maybeServeSubMeta answers ?format=meta and reports whether it handled the
// request. It reports true on failures too, so the caller stops rather than
// falling through to another format.
func (a *SUBController) maybeServeSubMeta(c *gin.Context) bool {
	if !strings.EqualFold(c.Query("format"), "meta") {
		return false
	}
	subId := c.Param("subid")
	_, host, _, _ := a.subService.ResolveRequest(c)

	// The config list is whatever the JSON subscription already generates, so
	// the two formats can never drift apart.
	jsonSub, header, err := a.subJsonService.GetJson(subId, host, true)
	if err != nil {
		writeSubError(c, err)
		return true
	}
	if len(jsonSub) == 0 {
		writeSubError(c, nil)
		return true
	}

	meta := loadSubMeta()
	masters := make([]SubMetaMaster, 0, len(meta.masters))
	for _, m := range meta.masters {
		masters = append(masters, SubMetaMaster{Domain: m.Domain, Ips: m.Ips})
	}
	payload := SubMetaPayload{
		Version: SubMetaVersion,
		// Generation time: the panel does not track a per-subscription content
		// revision, and a client only needs to order two documents it received.
		UpdatedAt: time.Now().Unix(),
		Meta: SubMetaInfo{
			Masters:      masters,
			EmergencyURL: meta.emergencyURL,
		},
		Outbounds: json.RawMessage(jsonSub),
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warning("sub: encoding the meta payload failed:", err)
		writeSubError(c, err)
		return true
	}

	setNoCacheHeaders(c)
	c.Writer.Header().Set("Subscription-Userinfo", header)
	c.Writer.Header().Set("Profile-Update-Interval", a.updateInterval)
	applyClusterHeaders(c, body)
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
	return true
}
