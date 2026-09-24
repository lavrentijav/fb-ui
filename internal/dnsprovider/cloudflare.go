package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

type cloudflare struct {
	zone    string
	token   string
	baseURL string
	hc      *http.Client

	mu     sync.Mutex
	zoneID string
}

func newCloudflare(zone, token, baseURL string) *cloudflare {
	if baseURL == "" {
		baseURL = cloudflareAPI
	}
	return &cloudflare{
		zone:    zone,
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

func (c *cloudflare) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("cloudflare: read response: %w", err)
	}
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("cloudflare: HTTP %d: undecodable response", resp.StatusCode)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return fmt.Errorf("cloudflare: HTTP %d: %s", resp.StatusCode, strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (c *cloudflare) zoneIDFor(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.zoneID != "" {
		return c.zoneID, nil
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(c.zone), nil, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if NormalizeName(z.Name) == c.zone {
			c.zoneID = z.ID
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: zone %q not visible to this token", c.zone)
}

func (c *cloudflare) list(ctx context.Context, zid, name, rtype string) ([]cfRecord, error) {
	q := url.Values{"name": {name}, "type": {rtype}, "per_page": {"100"}}
	var recs []cfRecord
	if err := c.do(ctx, http.MethodGet, "/zones/"+zid+"/dns_records?"+q.Encode(), nil, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

func (c *cloudflare) SetRecords(ctx context.Context, name, rtype string, values []string, ttl int) error {
	name, rtype = NormalizeName(name), strings.ToUpper(rtype)
	if !InZone(name, c.zone) {
		return fmt.Errorf("cloudflare: %q is outside zone %q", name, c.zone)
	}
	zid, err := c.zoneIDFor(ctx)
	if err != nil {
		return err
	}
	recs, err := c.list(ctx, zid, name, rtype)
	if err != nil {
		return err
	}
	have := make([]existingRecord, 0, len(recs))
	for _, r := range recs {
		have = append(have, existingRecord{ID: r.ID, Value: r.Content})
	}
	del, add := plan(rtype, have, values)
	if ttl <= 0 {
		ttl = 1 // Cloudflare's "automatic"
	}
	// Add before delete so a replaced address never leaves the name empty.
	for _, v := range add {
		content := v
		if rtype == "TXT" && !strings.HasPrefix(content, `"`) {
			content = `"` + content + `"`
		}
		rec := cfRecord{Type: rtype, Name: name, Content: content, TTL: ttl}
		if err := c.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", rec, nil); err != nil {
			return err
		}
	}
	for _, r := range del {
		if err := c.do(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+r.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *cloudflare) DeleteRecords(ctx context.Context, name, rtype string) error {
	return c.SetRecords(ctx, name, rtype, nil, 0)
}

func (c *cloudflare) Check(ctx context.Context) error {
	zid, err := c.zoneIDFor(ctx)
	if err != nil {
		return err
	}
	if zid == "" {
		return errors.New("cloudflare: empty zone id")
	}
	_, err = c.list(ctx, zid, c.zone, "NS")
	return err
}
