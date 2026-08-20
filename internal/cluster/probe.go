package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
)

// IdentityPath is appended to a peer's subscription path to reach its identity
// endpoint. Probing the subscription server (not the panel API) is deliberate:
// the only thing a fallback host has to be able to do is serve subscriptions.
const IdentityPath = "pubkey"

const (
	probeTimeout     = 4 * time.Second
	probeMaxBodySize = 4 << 10
)

// Identity is what a peer publishes about itself at IdentityPath.
type Identity struct {
	Alg       string `json:"alg"`
	PublicKey string `json:"key"`
}

// ProbeTarget is the addressing half of a peer, decoupled from the DB model.
type ProbeTarget struct {
	Scheme       string
	Domain       string
	Port         int
	SubPath      string
	AllowPrivate bool
}

// ProbeResult carries what one probe learned. LatencyMs is meaningful only when
// Err is nil.
type ProbeResult struct {
	LatencyMs int
	PublicKey string
	Err       error
}

// IdentityURL builds the absolute URL of a peer's identity endpoint.
func IdentityURL(t ProbeTarget) (string, error) {
	host, err := netsafe.NormalizeHost(t.Domain)
	if err != nil {
		return "", err
	}
	if t.Port <= 0 || t.Port > 65535 {
		return "", errors.New("cluster: peer port must be 1-65535")
	}
	scheme := t.Scheme
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(t.Port)),
		Path:   NormalizeSubPath(t.SubPath) + IdentityPath,
	}
	return u.String(), nil
}

// NormalizeSubPath forces the leading and trailing slash the URL builder above
// assumes, mirroring how the panel validates its own subPath setting.
func NormalizeSubPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/sub/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// Probe fetches a peer's identity endpoint and times the round trip.
func Probe(ctx context.Context, client *http.Client, t ProbeTarget) ProbeResult {
	target, err := IdentityURL(t)
	if err != nil {
		return ProbeResult{Err: err}
	}
	ctx, cancel := context.WithTimeout(netsafe.ContextWithAllowPrivate(ctx, t.AllowPrivate), probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ProbeResult{Err: err}
	}
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Err: err}
	}
	defer resp.Body.Close()
	latency := int(time.Since(start) / time.Millisecond)

	if resp.StatusCode != http.StatusOK {
		return ProbeResult{LatencyMs: latency, Err: errors.New("peer returned HTTP " + strconv.Itoa(resp.StatusCode))}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBodySize))
	if err != nil {
		return ProbeResult{LatencyMs: latency, Err: err}
	}
	var id Identity
	if err := json.Unmarshal(body, &id); err != nil {
		return ProbeResult{LatencyMs: latency, Err: errors.New("peer identity response is not JSON")}
	}
	return ProbeResult{LatencyMs: latency, PublicKey: id.PublicKey}
}

// NewProbeClient returns an HTTP client with the SSRF-guarded dialer, so a peer
// address pointing at a private range is refused unless explicitly allowed.
func NewProbeClient() *http.Client {
	return &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DialContext:         netsafe.SSRFGuardedDialContext,
			TLSHandshakeTimeout: probeTimeout,
		},
	}
}
