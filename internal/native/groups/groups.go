// Package groups keeps named sets of client emails for bulk operations:
// enabling, disabling, attaching to an inbound and resetting traffic, all at
// once. Membership is stored on the group, not on each client record, because
// a client's records are per node and an inbound.
package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// Table holds the groups, keyed by name.
const Table = "group"

var (
	ErrName    = errors.New("a group name is required (1–64 chars: letters, digits, space, . _ -)")
	ErrExists  = errors.New("a group with this name already exists")
	ErrNoGroup = errors.New("no such group")
	ErrNoEmail = errors.New("at least one email is required")
	validName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
)

// Group is one named set of client emails.
type Group struct {
	Name   string   `json:"name"`
	Remark string   `json:"remark,omitempty"`
	Emails []string `json:"emails"`
}

// Summary is a group with the numbers the page shows.
type Summary struct {
	Group
	Records     int   `json:"records"` // client records across nodes and inbounds
	Enabled     int   `json:"enabled"`
	Disabled    int   `json:"disabled"`
	Expired     int   `json:"expired"`
	TrafficUsed int64 `json:"trafficUsed"`
}

// Validate checks a group's shape.
func (g Group) Validate() error {
	if g.Name != strings.TrimSpace(g.Name) || !validName.MatchString(g.Name) {
		return ErrName
	}
	return nil
}

// Normalize trims and de-duplicates the email list, keeping order.
func Normalize(emails []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// Save writes a group, refusing a duplicate name unless overwrite.
func Save(ctx context.Context, h module.Host, g Group, overwrite bool) (Group, error) {
	g.Name = strings.TrimSpace(g.Name)
	if err := g.Validate(); err != nil {
		return Group{}, err
	}
	if _, exists := h.Get(Table, g.Name); exists && !overwrite {
		return Group{}, ErrExists
	}
	g.Emails = Normalize(g.Emails)
	if _, err := h.Set(ctx, Table, g.Name, map[string]any{
		"remark": g.Remark, "emails": g.Emails,
	}); err != nil {
		return Group{}, err
	}
	return g, nil
}

// Get answers one group.
func Get(r module.Reader, name string) (Group, bool) {
	rec, ok := r.Get(Table, name)
	if !ok {
		return Group{}, false
	}
	return decode(name, rec), true
}

// List answers every group, sorted by name.
func List(r module.Reader) []Group {
	var out []Group
	for _, name := range r.Keys(Table) {
		if rec, ok := r.Get(Table, name); ok {
			out = append(out, decode(name, rec))
		}
	}
	return out
}

func decode(name string, rec map[string]json.RawMessage) Group {
	g := Group{Name: name}
	_ = json.Unmarshal(rec["remark"], &g.Remark)
	_ = json.Unmarshal(rec["emails"], &g.Emails)
	return g
}

// Delete removes a group; the clients it named are untouched.
func Delete(ctx context.Context, h module.Host, name string) error {
	if _, ok := h.Get(Table, name); !ok {
		return ErrNoGroup
	}
	_, err := h.Delete(ctx, Table, name)
	return err
}

// SetMembers adds or removes emails and stores the group.
func SetMembers(ctx context.Context, h module.Host, name string, emails []string, add bool) (Group, error) {
	g, ok := Get(h, name)
	if !ok {
		return Group{}, ErrNoGroup
	}
	if len(Normalize(emails)) == 0 {
		return Group{}, ErrNoEmail
	}
	if add {
		g.Emails = Normalize(append(slices.Clone(g.Emails), emails...))
	} else {
		drop := map[string]bool{}
		for _, e := range Normalize(emails) {
			drop[e] = true
		}
		var kept []string
		for _, e := range g.Emails {
			if !drop[e] {
				kept = append(kept, e)
			}
		}
		g.Emails = kept
	}
	return Save(ctx, h, g, true)
}

// Summarize joins a group with the client records and the usage snapshots.
func Summarize(r module.Reader, g Group, now time.Time) Summary {
	sum := Summary{Group: g}
	in := map[string]bool{}
	for _, e := range g.Emails {
		in[e] = true
	}
	nowMs := now.UnixMilli()
	for _, c := range clients.List(r) {
		if !in[c.Email] {
			continue
		}
		sum.Records++
		if c.Enable {
			sum.Enabled++
		} else {
			sum.Disabled++
		}
		if c.ExpiresAt > 0 && nowMs >= c.ExpiresAt {
			sum.Expired++
		}
	}
	// usage is per email, counted across nodes, so it is added once per
	// member rather than once per record
	for _, e := range g.Emails {
		if rec, ok := r.Get(usage.Table, e); ok {
			var n int64
			if json.Unmarshal(rec["bytes"], &n) == nil {
				sum.TrafficUsed += n
			}
		}
	}
	return sum
}

// Summaries is every group's summary, sorted by name.
func Summaries(r module.Reader, now time.Time) []Summary {
	var out []Summary
	for _, g := range List(r) {
		out = append(out, Summarize(r, g, now))
	}
	return out
}

// SetEnabled turns every record of the group's emails on or off. It answers
// how many records changed.
func SetEnabled(ctx context.Context, h module.Host, name string, enable bool) (int, error) {
	g, ok := Get(h, name)
	if !ok {
		return 0, ErrNoGroup
	}
	in := map[string]bool{}
	for _, e := range g.Emails {
		in[e] = true
	}
	changed := 0
	for _, c := range clients.List(h) {
		if !in[c.Email] || c.Enable == enable {
			continue
		}
		c.Enable = enable
		if _, _, err := clients.Save(ctx, h, c); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// Attach creates a record for every group member missing on node/inbound.
// A member already there is left as it is, so the call is idempotent. The
// uuid comes from any existing record of the same email, so a member keeps
// one identity across inbounds.
func Attach(ctx context.Context, h module.Host, name, node, inbound, fallbackUUID string) (created int, err error) {
	g, ok := Get(h, name)
	if !ok {
		return 0, ErrNoGroup
	}
	byEmail := map[string]string{}
	for _, c := range clients.List(h) {
		if _, set := byEmail[c.Email]; !set {
			byEmail[c.Email] = c.UUID
		}
	}
	for _, email := range g.Emails {
		if _, exists := clients.Get(h, node, inbound, email); exists {
			continue
		}
		uuid := byEmail[email]
		if uuid == "" {
			uuid = fallbackUUID
		}
		if uuid == "" {
			return created, fmt.Errorf("%w: %s has no identity yet; create it on an inbound first", clients.ErrUUID, email)
		}
		c := clients.Client{Node: node, Inbound: inbound, Email: email, UUID: uuid, Enable: true}
		if _, err := clients.Create(ctx, h, c); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// Detach removes the group members' records from node/inbound.
func Detach(ctx context.Context, h module.Host, name, node, inbound string) (removed int, err error) {
	g, ok := Get(h, name)
	if !ok {
		return 0, ErrNoGroup
	}
	for _, email := range g.Emails {
		if _, exists := clients.Get(h, node, inbound, email); !exists {
			continue
		}
		if err := clients.Delete(ctx, h, node, inbound, email); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
