// Package outbound builds the client-side Xray outbound for one inbound and one
// of its clients: the object a subscription hands a user's app, and the one a
// cascade dials another panel with. Both need the same conversion, so it lives
// here rather than twice.
package outbound

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/goccy/go-json"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

// Stream turns an inbound's stored stream settings into the client-side view:
// server-only material dropped, TLS and REALITY reduced to what a dialer needs.
// clientKey seeds the per-client spiderX; finalMask is the panel-wide default
// merged into the result when set.
func Stream(raw string, clientKey string, finalMask string) map[string]any {
	var streamSettings map[string]any
	if err := json.Unmarshal([]byte(raw), &streamSettings); err != nil || streamSettings == nil {
		streamSettings = map[string]any{}
	}
	security, _ := streamSettings["security"].(string)
	switch security {
	case "tls":
		if tlsSettings, ok := streamSettings["tlsSettings"].(map[string]any); ok {
			streamSettings["tlsSettings"] = TLSData(tlsSettings)
		} else {
			delete(streamSettings, "tlsSettings")
		}
	case "reality":
		if realitySettings, ok := streamSettings["realitySettings"].(map[string]any); ok {
			streamSettings["realitySettings"] = RealityData(realitySettings, clientKey)
		} else {
			delete(streamSettings, "realitySettings")
		}
	}
	delete(streamSettings, "sockopt")

	if finalMask != "" {
		applyGlobalFinalMask(streamSettings, finalMask)
	}

	// remove proxy protocol
	network, _ := streamSettings["network"].(string)
	switch network {
	case "tcp":
		streamSettings["tcpSettings"] = removeAcceptProxy(streamSettings["tcpSettings"])
	case "ws":
		streamSettings["wsSettings"] = removeAcceptProxy(streamSettings["wsSettings"])
	case "httpupgrade":
		streamSettings["httpupgradeSettings"] = removeAcceptProxy(streamSettings["httpupgradeSettings"])
	case "xhttp":
		streamSettings["xhttpSettings"] = removeAcceptProxy(streamSettings["xhttpSettings"])
		if xhttp, ok := streamSettings["xhttpSettings"].(map[string]any); ok {
			delete(xhttp, "noSSEHeader")
			delete(xhttp, "scMaxBufferedPosts")
			delete(xhttp, "scStreamUpServerSecs")
			delete(xhttp, "serverMaxHeaderBytes")
			// Values matching xray-core's own defaults stay off the wire:
			// old panels seeded them into every stored config and the
			// literal scMinPostsIntervalMs=30 is a DPI fingerprint (#5141).
			if v, _ := xhttp["scMaxEachPostBytes"].(string); v == "" || v == "1000000" {
				delete(xhttp, "scMaxEachPostBytes")
			}
			if v, _ := xhttp["scMinPostsIntervalMs"].(string); v == "" || v == "30" {
				delete(xhttp, "scMinPostsIntervalMs")
			}
		}
	}
	return streamSettings
}

func applyGlobalFinalMask(streamSettings map[string]any, finalMask string) {
	var fm map[string]any
	if err := json.Unmarshal([]byte(finalMask), &fm); err != nil || len(fm) == 0 {
		return
	}
	merged := MergeFinalMask(streamSettings["finalmask"], fm)
	if len(merged) > 0 {
		streamSettings["finalmask"] = merged
	}
}

func removeAcceptProxy(setting any) map[string]any {
	netSettings, ok := setting.(map[string]any)
	if ok {
		delete(netSettings, "acceptProxyProtocol")
	}
	return netSettings
}

// TLSData reduces an inbound's tlsSettings to the fields a dialer needs; the
// certificates and other server-side material never leave the panel.
func TLSData(tData map[string]any) map[string]any {
	tlsData := make(map[string]any, 1)
	tlsClientSettings, _ := tData["settings"].(map[string]any)

	tlsData["serverName"] = tData["serverName"]
	tlsData["alpn"] = tData["alpn"]
	if fingerprint, ok := tlsClientSettings["fingerprint"].(string); ok {
		tlsData["fingerprint"] = fingerprint
	}
	if ech, ok := tlsClientSettings["echConfigList"].(string); ok && ech != "" {
		tlsData["echConfigList"] = ech
	}
	if vcn, ok := VerifyPeerCertByNameValue(tlsClientSettings); ok {
		tlsData["verifyPeerCertByName"] = vcn
	}
	// xray-core now parses pinnedPeerCertSha256 as a comma-separated string, not
	// an array; emit the joined form so v2ray clients can import the config (#5401).
	if pins, ok := PinnedSha256List(tlsClientSettings); ok {
		tlsData["pinnedPeerCertSha256"] = strings.Join(pins, ",")
	}
	return tlsData
}

// RealityData reduces an inbound's realitySettings the same way, picking one of
// the configured shortIds and serverNames for this dialer.
func RealityData(rData map[string]any, clientKey string) map[string]any {
	rltyData := make(map[string]any, 1)
	rltyClientSettings, _ := rData["settings"].(map[string]any)

	rltyData["show"] = false
	rltyData["publicKey"] = rltyClientSettings["publicKey"]
	rltyData["fingerprint"] = rltyClientSettings["fingerprint"]
	rltyData["mldsa65Verify"] = rltyClientSettings["mldsa65Verify"]

	seed, _ := rltyClientSettings["spiderX"].(string)
	rltyData["spiderX"] = DeriveSpiderX(seed, clientKey)
	shortIds, ok := rData["shortIds"].([]any)
	if ok && len(shortIds) > 0 {
		rltyData["shortId"], _ = shortIds[random.Num(len(shortIds))].(string)
	} else {
		rltyData["shortId"] = ""
	}
	serverNames, ok := rData["serverNames"].([]any)
	if ok && len(serverNames) > 0 {
		rltyData["serverName"], _ = serverNames[random.Num(len(serverNames))].(string)
	} else {
		rltyData["serverName"] = ""
	}

	return rltyData
}

// DeriveSpiderX maps the inbound's spiderX seed plus a stable client key to a
// deterministic per-client "/path"; frontend/src/lib/xray/spider-x.ts mirrors it.
func DeriveSpiderX(seed, clientKey string) string {
	if seed == "" && clientKey == "" {
		return "/" + random.Seq(15)
	}
	sum := sha256.Sum256([]byte(seed + "|" + clientKey))
	return "/" + hex.EncodeToString(sum[:])[:15]
}

// ClientKey is the stable per-client seed: the subscription id when the client
// has one, so every inbound of a subscription derives the same spiderX.
func ClientKey(c model.Client) string {
	if c.SubID != "" {
		return c.SubID
	}
	return c.Email
}

// Settings is the inbound's protocol settings without the client list — the
// half a dialer needs (method, decryption, server password).
func Settings(inbound *model.Inbound) map[string]any {
	shallow := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(inbound.Settings), &shallow)
	out := make(map[string]any, len(shallow))
	for key, raw := range shallow {
		if key == "clients" {
			continue
		}
		var value any
		_ = json.Unmarshal(raw, &value)
		out[key] = value
	}
	return out
}

func VerifyPeerCertByNameValue(tlsClientSettings any) (string, bool) {
	raw, ok := searchKey(tlsClientSettings, "verifyPeerCertByName")
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	if s = strings.TrimSpace(s); s == "" {
		return "", false
	}
	return s, true
}

// PinnedSha256List extracts tlsSettings.settings.pinnedPeerCertSha256 as a
// []string. The field is panel-only (stripped before the run-config reaches
// xray-core) but flows into share links so clients can pin the server's
// certificate hash.
func PinnedSha256List(tlsClientSettings any) ([]string, bool) {
	raw, ok := searchKey(tlsClientSettings, "pinnedPeerCertSha256")
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// searchKey walks a decoded JSON tree for the first value under key. A copy of
// the same walk internal/sub uses; it carries no protocol knowledge to drift.
func searchKey(data any, key string) (any, bool) {
	switch val := data.(type) {
	case map[string]any:
		for k, v := range val {
			if k == key {
				return v, true
			}
			if result, ok := searchKey(v, key); ok {
				return result, true
			}
		}
	case []any:
		for _, v := range val {
			if result, ok := searchKey(v, key); ok {
				return result, true
			}
		}
	}
	return nil, false
}
