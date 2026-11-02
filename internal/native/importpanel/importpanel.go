// Package importpanel reads the old panel's SQLite and plans how its entities
// become native records: a node's inbounds, and one client record per client
// and inbound. The plan is pure — a dry-run prints it, and the apply step only
// sends it to a master's admin API — so an operator can see exactly what will
// happen before anything changes.
package importpanel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, like the node's micro store
)

// Snapshot is what the old database holds that we can use.
type Snapshot struct {
	Inbounds      []Inbound
	Clients       []Client
	Links         []Link
	Nodes         map[int]string // old node id -> name
	Groups        []string
	Templates     int
	HostOverrides int
	Settings      int
	SkippedTables []string
}

// Inbound is one row of the old panel's inbounds.
type Inbound struct {
	ID        int
	Tag       string
	Remark    string
	Port      int
	Protocol  string
	Settings  string
	Stream    string
	NodeID    int // 0 = the old panel itself
	Enable    bool
	ShareAddr string
	ShareMode string
}

// Client is one row of the old panel's clients.
type Client struct {
	Email      string
	UUID       string
	TotalBytes int64
	ExpiresAt  int64 // unix ms
	LimitIP    int
	Enable     bool
	Group      string
	SubID      string
}

// Link attaches a client to an inbound.
type Link struct {
	Email     string
	InboundID int
}

// Read opens the old database read-only and loads what the plan needs.
func Read(path string) (*Snapshot, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("importpanel: %s: %w", path, err)
	}
	s := &Snapshot{Nodes: map[int]string{}}
	if err := readInbounds(db, s); err != nil {
		return nil, err
	}
	if err := readClients(db, s); err != nil {
		return nil, err
	}
	if err := readLinks(db, s); err != nil {
		return nil, err
	}
	if err := readNodes(db, s); err != nil {
		return nil, err
	}
	s.Groups = readStrings(db, "SELECT name FROM client_groups ORDER BY name")
	s.Templates = count(db, "inbound_templates")
	s.HostOverrides = count(db, "hosts")
	s.Settings = count(db, "settings")
	for _, t := range []struct {
		table, why string
	}{
		{"certificates", "certificates are re-issued by the native masters"},
		{"dns_providers", "DNS accounts are re-created in the master's state"},
		{"filter_rules", "routing rules move to the native routing module"},
		{"client_hwids", "HWID registrations are device state, not config"},
		{"users", "panel logins are replaced by API tokens and sessions"},
	} {
		if count(db, t.table) > 0 {
			s.SkippedTables = append(s.SkippedTables, t.table+" ("+t.why+")")
		}
	}
	return s, nil
}

func readInbounds(db *sql.DB, s *Snapshot) error {
	rows, err := db.Query(`SELECT id, COALESCE(tag,''), COALESCE(remark,''), COALESCE(port,0),
		COALESCE(protocol,''), COALESCE(settings,''), COALESCE(stream_settings,''),
		COALESCE(node_id,0), COALESCE(enable,1),
		COALESCE(share_addr_strategy,'node'), COALESCE(share_addr,'')
		FROM inbounds ORDER BY id`)
	if err != nil {
		return fmt.Errorf("importpanel: inbounds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var in Inbound
		if err := rows.Scan(&in.ID, &in.Tag, &in.Remark, &in.Port, &in.Protocol, &in.Settings,
			&in.Stream, &in.NodeID, &in.Enable, &in.ShareMode, &in.ShareAddr); err != nil {
			return err
		}
		s.Inbounds = append(s.Inbounds, in)
	}
	return rows.Err()
}

func readClients(db *sql.DB, s *Snapshot) error {
	rows, err := db.Query(`SELECT COALESCE(email,''), COALESCE(uuid,''), COALESCE(total_gb,0),
		COALESCE(expiry_time,0), COALESCE(limit_ip,0), COALESCE(enable,1),
		COALESCE(group_name,''), COALESCE(sub_id,'')
		FROM clients ORDER BY id`)
	if err != nil {
		return fmt.Errorf("importpanel: clients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.Email, &c.UUID, &c.TotalBytes, &c.ExpiresAt, &c.LimitIP,
			&c.Enable, &c.Group, &c.SubID); err != nil {
			return err
		}
		s.Clients = append(s.Clients, c)
	}
	return rows.Err()
}

func readLinks(db *sql.DB, s *Snapshot) error {
	rows, err := db.Query(`SELECT c.email, ci.inbound_id FROM client_inbounds ci
		JOIN clients c ON c.id = ci.client_id ORDER BY ci.inbound_id, c.email`)
	if err != nil {
		// an older panel attached clients inside the inbound's settings JSON
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Email, &l.InboundID); err != nil {
			return err
		}
		s.Links = append(s.Links, l)
	}
	return rows.Err()
}

func readNodes(db *sql.DB, s *Snapshot) error {
	rows, err := db.Query(`SELECT id, name FROM nodes`)
	if err != nil {
		return nil // a panel without node support
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		s.Nodes[id] = name
	}
	return rows.Err()
}

func readStrings(db *sql.DB, query string) []string {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func count(db *sql.DB, table string) int {
	var n int
	if db.QueryRow("SELECT count(*) FROM "+table).Scan(&n) != nil {
		return 0
	}
	return n
}

// Options are what the operator knows and the database does not.
type Options struct {
	// LocalNode is the native node that takes the old panel's own inbounds
	// (those with no node id). Required when there are any.
	LocalNode string
	// NodeMap renames old node ids; the default is the node's name.
	NodeMap map[int]string
	// Profile is the default transport when the stream settings name none.
	Profile string
}

// PlannedInbound is a native inbound plus the old id it came from.
type PlannedInbound struct {
	OldID   int
	Node    string
	Tag     string
	Port    int
	Network string
	Path    string
	Host    string
	Remarks []string
}

// PlannedClient is one native client record (per node and inbound).
type PlannedClient struct {
	Node       string `json:"node"`
	Inbound    string `json:"inbound"`
	Email      string `json:"email"`
	UUID       string `json:"uuid"`
	Enable     bool   `json:"enable"`
	TotalBytes int64  `json:"totalBytes,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
	LimitIP    int    `json:"limitIp,omitempty"`
}

// Skip is an entity that does not map, with the reason.
type Skip struct{ Entity, Reason string }

// Report is the dry-run result.
type Report struct {
	Inbounds []PlannedInbound `json:"inbounds"`
	Clients  []PlannedClient  `json:"clients"`
	Groups   []string         `json:"groups"`
	Skipped  []Skip           `json:"skipped"`
	Notes    []string         `json:"notes"`
}

var validTag = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Plan maps the snapshot onto native records.
func Plan(s *Snapshot, opt Options) Report {
	var r Report
	r.Groups = append(r.Groups, s.Groups...)
	if opt.Profile == "" {
		opt.Profile = "ws"
	}
	clients := map[string]Client{}
	for _, c := range s.Clients {
		clients[c.Email] = c
	}
	byInbound := map[int][]string{}
	for _, l := range s.Links {
		byInbound[l.InboundID] = append(byInbound[l.InboundID], l.Email)
	}
	tagOf := map[int]string{}

	for _, in := range s.Inbounds {
		if !in.Enable {
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q", in.Tag), "disabled in the old panel"})
			continue
		}
		if strings.ToLower(in.Protocol) != "vless" {
			r.Skipped = append(r.Skipped, Skip{
				fmt.Sprintf("inbound %q (%s)", in.Tag, in.Protocol),
				"the native node model runs VLESS only; port it as a sidecar later",
			})
			continue
		}
		node, ok := nodeFor(in, s, opt)
		if !ok {
			r.Skipped = append(r.Skipped, Skip{
				fmt.Sprintf("inbound %q", in.Tag),
				"it belongs to the old panel itself and -local-node was not given",
			})
			continue
		}
		network, path, hostHeader := transport(in)
		if network != "ws" && network != "xhttp" {
			r.Skipped = append(r.Skipped, Skip{
				fmt.Sprintf("inbound %q", in.Tag),
				fmt.Sprintf("transport %q has no native equivalent yet", network),
			})
			continue
		}
		tag := nativeTag(in)
		if !validTag.MatchString(tag) {
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q", in.Tag), "the tag cannot be used as a native inbound tag"})
			continue
		}
		host := in.ShareAddr
		if host == "" || strings.EqualFold(in.ShareMode, "node") {
			host = hostHeader
		}
		if host == "" {
			host = node
		}
		pi := PlannedInbound{
			OldID: in.ID, Node: node, Tag: tag, Port: in.Port,
			Network: network, Path: path, Host: host, Remarks: []string{in.Remark},
		}
		tagOf[in.ID] = tag
		r.Inbounds = append(r.Inbounds, pi)

		for _, email := range byInbound[in.ID] {
			c, ok := clients[email]
			if !ok {
				r.Skipped = append(r.Skipped, Skip{"client " + email, "attached to an inbound but missing from the clients table"})
				continue
			}
			r.Clients = append(r.Clients, PlannedClient{
				Node: node, Inbound: tag, Email: c.Email, UUID: c.UUID, Enable: c.Enable,
				TotalBytes: c.TotalBytes, ExpiresAt: c.ExpiresAt, LimitIP: c.LimitIP,
			})
		}
	}

	// clients with no link row: an older panel kept them in the inbound's
	// settings, which Read cannot split; report them rather than lose them
	linked := map[string]bool{}
	for _, l := range s.Links {
		linked[l.Email] = true
	}
	unattached := 0
	for _, c := range s.Clients {
		if !linked[c.Email] && c.Email != "" {
			unattached++
		}
	}
	if unattached > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"%d client(s) have no client_inbounds row; if the panel predates that table they live in each inbound's settings JSON and must be linked by hand",
			unattached))
	}
	if s.Settings > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d settings row(s) are not imported: the native master keeps its own typed settings", s.Settings))
	}
	for _, t := range s.SkippedTables {
		r.Notes = append(r.Notes, "not imported: "+t)
	}
	if s.Templates > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d inbound template(s) are re-created through the native templates module", s.Templates))
	}
	if s.HostOverrides > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d host override(s) map to the native hosts module one address at a time", s.HostOverrides))
	}
	return r
}

// nodeFor answers which native node takes this inbound.
func nodeFor(in Inbound, s *Snapshot, opt Options) (string, bool) {
	if in.NodeID == 0 {
		if opt.LocalNode == "" {
			return "", false
		}
		return opt.LocalNode, true
	}
	if name, ok := opt.NodeMap[in.NodeID]; ok && name != "" {
		return name, true
	}
	if name, ok := s.Nodes[in.NodeID]; ok && name != "" {
		return name, true
	}
	return "node" + strconv.Itoa(in.NodeID), true
}

// transport reads the ws/xhttp shape from the old stream settings.
func transport(in Inbound) (network, path, host string) {
	var st map[string]any
	if json.Unmarshal([]byte(in.Stream), &st) != nil {
		return "ws", "/", ""
	}
	network, _ = st["network"].(string)
	if network == "" {
		network = "ws"
	}
	settingsKey := network + "Settings"
	if network == "ws" {
		settingsKey = "wsSettings"
	}
	if network == "xhttp" {
		settingsKey = "xhttpSettings"
	}
	sub, _ := st[settingsKey].(map[string]any)
	path, _ = sub["path"].(string)
	if path == "" {
		path = "/"
	}
	if hdr, ok := sub["headers"].(map[string]any); ok {
		switch v := hdr["Host"].(type) {
		case string:
			host = v
		case []any:
			if len(v) > 0 {
				host, _ = v[0].(string)
			}
		}
	}
	if host == "" {
		host, _ = sub["host"].(string)
	}
	return network, path, host
}

// nativeTag keeps a usable old tag, or derives one from the remark.
func nativeTag(in Inbound) string {
	if validTag.MatchString(strings.ToLower(in.Tag)) && in.Tag == strings.ToLower(in.Tag) {
		return in.Tag
	}
	var b strings.Builder
	for _, r := range strings.ToLower(in.Remark) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	tag := strings.Trim(b.String(), "-_")
	if tag == "" || !validTag.MatchString(tag) {
		tag = fmt.Sprintf("in-%d", in.Port)
	}
	return tag
}
