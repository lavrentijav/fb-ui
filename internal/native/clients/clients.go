// Package clients keeps the people a node's inbounds serve: one record per
// node, inbound and email, with its traffic allowance and expiry. A master
// adds the live ones to the inbound when it renders the node's spec.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

// Table holds the records, keyed "<node>/<inbound>/<email>".
const Table = "client"

var (
	ErrEmail    = errors.New("an email is required (no '/', '>>>', and at most 128 characters)")
	ErrTarget   = errors.New("a node and an inbound tag are required")
	ErrUUID     = errors.New("a valid UUID is required")
	ErrBytes    = errors.New("the traffic limit must be non-negative")
	ErrExists   = errors.New("this email is already on that inbound")
	ErrNoClient = errors.New("no such client")
	validUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	validTag    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// Client is one person on one inbound of one node.
type Client struct {
	Node    string `json:"node"`
	Inbound string `json:"inbound"`
	Email   string `json:"email"`
	UUID    string `json:"uuid"`
	// Enable false keeps the record but leaves it out of the node's config.
	Enable bool `json:"enable"`
	// TotalGB is the traffic allowance in bytes; 0 means unlimited.
	TotalGB int64 `json:"totalBytes,omitempty"`
	// ExpiresAt is unix ms; 0 means never.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// LimitIP caps concurrent addresses; 0 means no limit.
	LimitIP int    `json:"limitIp,omitempty"`
	Remark  string `json:"remark,omitempty"`
}

// Validate checks a record before it is stored.
func (c Client) Validate() error {
	if c.Node == "" || !validTag.MatchString(c.Inbound) {
		return ErrTarget
	}
	if c.Email == "" || len(c.Email) > 128 || strings.ContainsAny(c.Email, "/") || strings.Contains(c.Email, ">>>") {
		return ErrEmail
	}
	if !validUUID.MatchString(strings.ToLower(c.UUID)) {
		return ErrUUID
	}
	if c.TotalGB < 0 || c.LimitIP < 0 {
		return ErrBytes
	}
	return nil
}

// Key is the op log key of a record.
func Key(node, inbound, email string) string { return node + "/" + inbound + "/" + email }

// Save writes a record; Create is false when it already existed, so a caller
// can tell a create from an overwrite.
func Save(ctx context.Context, h module.Host, c Client) (saved Client, created bool, err error) {
	if err := c.Validate(); err != nil {
		return Client{}, false, err
	}
	c.UUID = strings.ToLower(c.UUID)
	k := Key(c.Node, c.Inbound, c.Email)
	_, existed := h.Get(Table, k)
	if _, err := h.Set(ctx, Table, k, map[string]any{
		"key":  k,
		"node": c.Node, "inbound": c.Inbound, "email": c.Email, "uuid": c.UUID,
		"enable": c.Enable, "totalBytes": c.TotalGB, "expiresAt": c.ExpiresAt,
		"limitIp": c.LimitIP, "remark": c.Remark,
	}); err != nil {
		return Client{}, false, err
	}
	return c, !existed, nil
}

// Create refuses an email already present on the same inbound.
func Create(ctx context.Context, h module.Host, c Client) (Client, error) {
	if _, exists := h.Get(Table, Key(c.Node, c.Inbound, c.Email)); exists {
		return Client{}, ErrExists
	}
	saved, _, err := Save(ctx, h, c)
	return saved, err
}

// Get answers one record.
func Get(r module.Reader, node, inbound, email string) (Client, bool) {
	rec, ok := r.Get(Table, Key(node, inbound, email))
	if !ok {
		return Client{}, false
	}
	return decode(rec), true
}

// Delete removes one record; a missing one is not an error.
func Delete(ctx context.Context, h module.Host, node, inbound, email string) error {
	_, err := h.Delete(ctx, Table, Key(node, inbound, email))
	return err
}

// List answers every record, sorted by node, inbound and email.
func List(r module.Reader) []Client {
	var out []Client
	for _, k := range r.Keys(Table) {
		if rec, ok := r.Get(Table, k); ok {
			out = append(out, decode(rec))
		}
	}
	return out
}

func decode(rec map[string]json.RawMessage) Client {
	var c Client
	str := func(name string, dst *string) { _ = json.Unmarshal(rec[name], dst) }
	num := func(name string, dst *int64) { _ = json.Unmarshal(rec[name], dst) }
	str("node", &c.Node)
	str("inbound", &c.Inbound)
	str("email", &c.Email)
	str("uuid", &c.UUID)
	str("remark", &c.Remark)
	var enable bool
	_ = json.Unmarshal(rec["enable"], &enable)
	c.Enable = enable
	num("totalBytes", &c.TotalGB)
	num("expiresAt", &c.ExpiresAt)
	var limit int64
	num("limitIp", &limit)
	c.LimitIP = int(limit)
	return c
}

// Active reports whether the record should reach the node now: enabled and
// not past its expiry. Traffic exhaustion is enforced by the node's counters
// (a later step); a missing usage figure never hides a client.
func (c Client) Active(now time.Time) bool {
	return c.Enable && (c.ExpiresAt == 0 || now.UnixMilli() < c.ExpiresAt)
}

// Attach adds the node's active clients to its inbounds. A client whose
// inbound is not in the spec is left out: the record outlives the inbound.
func Attach(r module.Reader, node string, spec *nodespec.Spec, now time.Time) {
	byInbound := map[string][]Client{}
	for _, c := range List(r) {
		if c.Node == node && c.Active(now) {
			byInbound[c.Inbound] = append(byInbound[c.Inbound], c)
		}
	}
	for i := range spec.Inbounds {
		ib := &spec.Inbounds[i]
		have := map[string]bool{}
		for _, existing := range ib.Clients {
			have[existing.Email] = true
		}
		for _, c := range byInbound[ib.Tag] {
			if have[c.Email] {
				continue // a network member already added to this inbound
			}
			ib.Clients = append(ib.Clients, nodespec.Client{
				Email: c.Email, ID: c.UUID, LimitIP: c.LimitIP,
			})
		}
	}
}

// ParseExpiry accepts an RFC3339 time or a unix millisecond count; "" is 0.
func ParseExpiry(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("%w: expiry is negative", ErrBytes)
		}
		return n, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, fmt.Errorf("expiry must be RFC3339 or unix ms: %w", err)
	}
	return t.UnixMilli(), nil
}
