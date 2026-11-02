// Package hosts keeps the extra addresses a subscription hands out for an
// inbound: a CDN name, a spare IP, another port — anything a client can dial
// instead of the node's own public name.
package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/sub"
)

// Table holds the entries in the op log, keyed "<node>/<inbound>/<id>".
const Table = "subhost"

var (
	ErrBadAddress = errors.New("an address is required (a host name or an IP, no scheme)")
	ErrBadPort    = errors.New("the port must be 1–65535 (0 means 443)")
	ErrBadKind    = errors.New("security must be tls or none")
	ErrNoNode     = errors.New("a node and an inbound tag are required")
	hostName      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,252}[A-Za-z0-9])?$`)
)

// Entry is one advertised endpoint for a node's inbound.
type Entry struct {
	ID       string `json:"id"`
	Node     string `json:"node"`
	Inbound  string `json:"inbound"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Security string `json:"security,omitempty"` // tls | none
	SNI      string `json:"sni,omitempty"`
	Host     string `json:"host,omitempty"`
	Path     string `json:"path,omitempty"`
	Remark   string `json:"remark,omitempty"`
	Enable   bool   `json:"enable"`
}

// Validate checks one entry; the defaults (443, tls) are applied by the caller.
func (e Entry) Validate() error {
	if e.Node == "" || e.Inbound == "" {
		return ErrNoNode
	}
	if e.Address == "" || strings.Contains(e.Address, "://") {
		return ErrBadAddress
	}
	if net.ParseIP(e.Address) == nil && !hostName.MatchString(e.Address) {
		return ErrBadAddress
	}
	if e.Port < 0 || e.Port > 65535 {
		return ErrBadPort
	}
	if e.Security != "" && e.Security != "tls" && e.Security != "none" {
		return ErrBadKind
	}
	return nil
}

func key(node, inbound, id string) string { return node + "/" + inbound + "/" + id }

// Save writes entry and returns it; id empty makes a new one.
func Save(ctx context.Context, h module.Host, e Entry) (Entry, error) {
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	if e.ID == "" {
		e.ID = h.NextKey()
	}
	if _, err := h.Set(ctx, Table, key(e.Node, e.Inbound, e.ID), map[string]any{
		"id": e.ID, "node": e.Node, "inbound": e.Inbound, "address": e.Address, "port": e.Port,
		"security": e.Security, "sni": e.SNI, "host": e.Host, "path": e.Path,
		"remark": e.Remark, "enable": e.Enable,
	}); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Delete drops one entry.
func Delete(ctx context.Context, h module.Host, e Entry) error {
	_, err := h.Delete(ctx, Table, key(e.Node, e.Inbound, e.ID))
	return err
}

// List is every entry, sorted by node, inbound and id.
func List(r module.Reader) []Entry {
	var out []Entry
	for _, k := range r.Keys(Table) {
		rec, ok := r.Get(Table, k)
		if !ok {
			continue
		}
		out = append(out, decode(rec))
	}
	// Keys is already sorted, so the entries come out grouped by node/inbound
	return out
}

func decode(rec map[string]json.RawMessage) Entry {
	var e Entry
	var port int
	_ = json.Unmarshal(rec["node"], &e.Node)
	_ = json.Unmarshal(rec["inbound"], &e.Inbound)
	_ = json.Unmarshal(rec["address"], &e.Address)
	_ = json.Unmarshal(rec["port"], &port)
	e.Port = port
	_ = json.Unmarshal(rec["security"], &e.Security)
	_ = json.Unmarshal(rec["sni"], &e.SNI)
	_ = json.Unmarshal(rec["host"], &e.Host)
	_ = json.Unmarshal(rec["path"], &e.Path)
	_ = json.Unmarshal(rec["remark"], &e.Remark)
	_ = json.Unmarshal(rec["enable"], &e.Enable)
	_ = json.Unmarshal(rec["id"], &e.ID)
	return e
}

// Endpoints turns the enabled entries into the subscription's hosts map.
func Endpoints(entries []Entry) sub.Hosts {
	out := sub.Hosts{}
	for _, e := range entries {
		if !e.Enable {
			continue
		}
		k := e.Node + "/" + e.Inbound
		out[k] = append(out[k], sub.Endpoint{
			Address: e.Address, Port: e.Port, Security: e.Security,
			SNI: e.SNI, Host: e.Host, Path: e.Path, Remark: e.Remark,
		})
	}
	return out
}

// Describe is a short human summary.
func (e Entry) Describe() string {
	port := e.Port
	if port == 0 {
		port = 443
	}
	return fmt.Sprintf("%s %s:%s", e.Node, e.Address, strconv.Itoa(port))
}
