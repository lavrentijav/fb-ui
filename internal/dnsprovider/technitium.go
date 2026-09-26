package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// technitium drives a self-hosted Technitium DNS Server through its HTTP API
// with an API token created in its web console (Administration → Sessions).
type technitium struct {
	zone    string
	token   string
	baseURL string
	hc      *http.Client
}

func newTechnitium(zone, token, baseURL string) *technitium {
	return &technitium{
		zone:    zone,
		token:   token,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

type tnEnvelope struct {
	Status       string          `json:"status"`
	ErrorMessage string          `json:"errorMessage"`
	Response     json.RawMessage `json:"response"`
}

type tnRecord struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	RData json.RawMessage `json:"rData"`
}

// call POSTs a form so the token never lands in a proxy's request-line log.
func (t *technitium) call(ctx context.Context, path string, form url.Values, out any) error {
	form.Set("token", t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("technitium: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("technitium: read response: %w", err)
	}
	var env tnEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("technitium: HTTP %d: undecodable response", resp.StatusCode)
	}
	switch env.Status {
	case "ok":
	case "invalid-token":
		return fmt.Errorf("technitium: invalid or expired API token")
	default:
		return fmt.Errorf("technitium: %s: %s", env.Status, env.ErrorMessage)
	}
	if out != nil && len(env.Response) > 0 {
		return json.Unmarshal(env.Response, out)
	}
	return nil
}

// valueParam is the form field Technitium uses for a record's data per type.
func valueParam(rtype string) (string, error) {
	switch rtype {
	case "A", "AAAA":
		return "ipAddress", nil
	case "TXT":
		return "text", nil
	case "CNAME":
		return "cname", nil
	default:
		return "", fmt.Errorf("technitium: record type %q is not supported", rtype)
	}
}

func recordValue(rtype string, rdata json.RawMessage) string {
	var d struct {
		IPAddress string `json:"ipAddress"`
		Text      string `json:"text"`
		CNAME     string `json:"cname"`
	}
	_ = json.Unmarshal(rdata, &d)
	switch rtype {
	case "A", "AAAA":
		return d.IPAddress
	case "TXT":
		return d.Text
	case "CNAME":
		return d.CNAME
	}
	return ""
}

func (t *technitium) list(ctx context.Context, name, rtype string) ([]existingRecord, error) {
	var resp struct {
		Records []tnRecord `json:"records"`
	}
	form := url.Values{"domain": {name}, "zone": {t.zone}, "listZone": {"false"}}
	if err := t.call(ctx, "/api/zones/records/get", form, &resp); err != nil {
		return nil, err
	}
	var out []existingRecord
	for _, r := range resp.Records {
		if strings.EqualFold(r.Type, rtype) && NormalizeName(r.Name) == name {
			out = append(out, existingRecord{Value: recordValue(rtype, r.RData)})
		}
	}
	return out, nil
}

func (t *technitium) SetRecords(ctx context.Context, name, rtype string, values []string, ttl int) error {
	name, rtype = NormalizeName(name), strings.ToUpper(rtype)
	if !InZone(name, t.zone) {
		return fmt.Errorf("technitium: %q is outside zone %q", name, t.zone)
	}
	param, err := valueParam(rtype)
	if err != nil {
		return err
	}
	have, err := t.list(ctx, name, rtype)
	if err != nil {
		return err
	}
	del, add := plan(rtype, have, values)
	if ttl <= 0 {
		ttl = 300
	}
	for _, v := range add {
		form := url.Values{
			"domain": {name}, "zone": {t.zone}, "type": {rtype},
			"ttl": {strconv.Itoa(ttl)}, param: {v},
		}
		if err := t.call(ctx, "/api/zones/records/add", form, nil); err != nil {
			return err
		}
	}
	for _, r := range del {
		form := url.Values{"domain": {name}, "zone": {t.zone}, "type": {rtype}, param: {r.Value}}
		if err := t.call(ctx, "/api/zones/records/delete", form, nil); err != nil {
			return err
		}
	}
	return nil
}

func (t *technitium) DeleteRecords(ctx context.Context, name, rtype string) error {
	return t.SetRecords(ctx, name, rtype, nil, 0)
}

func (t *technitium) Check(ctx context.Context) error {
	if t.zone == "" {
		_, err := t.listZones(ctx)
		return err
	}
	_, err := t.list(ctx, t.zone, "SOA")
	return err
}

// listZones keeps primary zones only: secondary and stub zones are
// read-only copies, and internal ones (localhost, reverse) are not ours.
func (t *technitium) listZones(ctx context.Context) ([]string, error) {
	var resp struct {
		Zones []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Internal bool   `json:"internal"`
			Disabled bool   `json:"disabled"`
		} `json:"zones"`
	}
	if err := t.call(ctx, "/api/zones/list", url.Values{}, &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, z := range resp.Zones {
		if strings.EqualFold(z.Type, "Primary") && !z.Internal && !z.Disabled {
			out = append(out, z.Name)
		}
	}
	return out, nil
}
