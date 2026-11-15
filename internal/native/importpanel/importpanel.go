// Package importpanel reads the old panel's SQLite and plans how its entities
// become native records: a node's inbounds, and one client record per client
// and inbound. The plan is pure — a dry-run prints it, and the apply step only
// sends it to a master's admin API — so an operator can see exactly what will
// happen before anything changes.
package importpanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, like the node's micro store
)

// Schema is what the old database looks like: which tables are present and
// where the clients live. It is reported so an operator can tell an upstream
// panel (clients inside inbounds.settings) from this fork (client records).
type Schema struct {
	Tables          []string `json:"tables"`
	Inbounds        int      `json:"inbounds"`
	Clients         int      `json:"clients"`
	ClientInbounds  int      `json:"clientInbounds"`
	Nodes           int      `json:"nodes"`
	Groups          int      `json:"groups"`
	SettingsClients int      `json:"settingsClients"`
	Style           string   `json:"style"`
}

// Snapshot is what the old database holds that we can use.
type Snapshot struct {
	Inbounds      []Inbound
	Clients       []Client
	Links         []Link
	Nodes         map[int]string // old node id -> name
	Groups        []string
	GroupMembers  map[string][]string
	Schema        Schema
	Templates     int
	HostOverrides int
	Settings      int
	SkippedTables []string

	seenLinks map[string]bool
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

// Client is one row of the old panel's clients, wherever it was stored.
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

// Read opens the old database read-only and loads what the plan needs. It
// understands both shapes: upstream stores the clients inside each inbound's
// settings JSON, this fork keeps a clients table plus client_inbounds.
func Read(path string) (*Snapshot, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("importpanel: %s: %w", path, err)
	}
	s := &Snapshot{Nodes: map[int]string{}, GroupMembers: map[string][]string{}, seenLinks: map[string]bool{}}
	tables, err := tableNames(db)
	if err != nil {
		return nil, err
	}
	if !tables["inbounds"] {
		return nil, fmt.Errorf("importpanel: %s: not a panel database (no inbounds table)", path)
	}
	s.Schema.Tables = make([]string, 0, len(tables))
	for name := range tables {
		s.Schema.Tables = append(s.Schema.Tables, name)
	}
	slices.Sort(s.Schema.Tables)

	if err := readInbounds(db, s); err != nil {
		return nil, err
	}
	if err := readClients(db, s, tables); err != nil {
		return nil, err
	}
	if err := readLinks(db, s, tables); err != nil {
		return nil, err
	}
	readSettingsClients(s)
	if err := readNodes(db, s, tables); err != nil {
		return nil, err
	}
	readGroups(db, s, tables)
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
	s.Schema.Inbounds = len(s.Inbounds)
	s.Schema.Nodes = len(s.Nodes)
	s.Schema.Groups = len(s.Groups)
	s.Schema.Style = schemaStyle(s.Schema)
	return s, nil
}

func schemaStyle(s Schema) string {
	switch {
	case s.Clients > 0 && s.SettingsClients > 0:
		return "mixed"
	case s.Clients > 0:
		return "client-records"
	case s.SettingsClients > 0:
		return "upstream-settings"
	default:
		return "empty"
	}
}

// tableNames lists the user tables, so a missing table is a shape and not an
// error. Views are ignored.
func tableNames(db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("importpanel: schema: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// columns names the columns of a table; an absent table has none.
func columns(db *sql.DB, table string) map[string]bool {
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// pick returns COALESCE(col, fallback) when the column exists, else fallback.
func pick(cols map[string]bool, name, fallback string) string {
	if cols[name] {
		return "COALESCE(" + name + ", " + fallback + ")"
	}
	return fallback
}

func readInbounds(db *sql.DB, s *Snapshot) error {
	cols := columns(db, "inbounds")
	query := `SELECT COALESCE(id, 0), ` + pick(cols, "tag", "''") + `, ` + pick(cols, "remark", "''") + `,
		` + pick(cols, "port", "0") + `, ` + pick(cols, "protocol", "''") + `, ` + pick(cols, "settings", "''") + `,
		` + pick(cols, "stream_settings", "''") + `, ` + pick(cols, "node_id", "0") + `,
		` + pick(cols, "enable", "1") + `, ` + pick(cols, "share_addr_strategy", "'node'") + `,
		` + pick(cols, "share_addr", "''") + ` FROM inbounds ORDER BY id`
	rows, err := db.QueryContext(context.Background(), query)
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

func readClients(db *sql.DB, s *Snapshot, tables map[string]bool) error {
	s.Schema.ClientInbounds = count(db, "client_inbounds")
	if !tables["clients"] {
		return nil
	}
	cols := columns(db, "clients")
	if !cols["email"] {
		return nil
	}
	n := count(db, "clients")
	s.Schema.Clients = n
	if n == 0 {
		return nil
	}
	query := `SELECT ` + pick(cols, "email", "''") + `, ` + pick(cols, "uuid", "''") + `,
		` + pick(cols, "total_gb", "0") + `, ` + pick(cols, "expiry_time", "0") + `,
		` + pick(cols, "limit_ip", "0") + `, ` + pick(cols, "enable", "1") + `,
		` + pick(cols, "group_name", "''") + `, ` + pick(cols, "sub_id", "''") + `
		FROM clients ORDER BY id`
	rows, err := db.QueryContext(context.Background(), query)
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
		ensureClient(s, c)
	}
	return rows.Err()
}

func readLinks(db *sql.DB, s *Snapshot, tables map[string]bool) error {
	if !tables["client_inbounds"] || !tables["clients"] {
		return nil
	}
	cols := columns(db, "client_inbounds")
	if !cols["client_id"] || !cols["inbound_id"] {
		return nil
	}
	rows, err := db.QueryContext(context.Background(), `SELECT c.email, ci.inbound_id FROM client_inbounds ci
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
		addLink(s, l.Email, l.InboundID)
	}
	return rows.Err()
}

// settingsClient is one entry of an inbound's settings.clients array; the
// credential field differs by protocol (id for VLESS/VMess, password for the
// rest), and an absent enable means true.
type settingsClient struct {
	ID         string `json:"id"`
	UUID       string `json:"uuid"`
	Email      string `json:"email"`
	Enable     *bool  `json:"enable"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	LimitIP    int    `json:"limitIp"`
	SubID      string `json:"subId"`
	Group      string `json:"group"`
}

// readSettingsClients extracts the upstream shape: clients inside each
// inbound's settings JSON. A client already present in the clients table wins,
// so a half-migrated database does not duplicate records.
func readSettingsClients(s *Snapshot) {
	for i := range s.Inbounds {
		in := &s.Inbounds[i]
		for _, sc := range settingsClients(in.Settings) {
			s.Schema.SettingsClients++
			if sc.Email == "" {
				continue
			}
			ensureClient(s, sc)
			addLink(s, sc.Email, in.ID)
		}
	}
}

func settingsClients(settings string) []Client {
	var doc struct {
		Clients []settingsClient `json:"clients"`
	}
	if json.Unmarshal([]byte(settings), &doc) != nil {
		return nil
	}
	out := make([]Client, 0, len(doc.Clients))
	for _, c := range doc.Clients {
		uuid := c.ID
		if uuid == "" {
			uuid = c.UUID
		}
		enable := true
		if c.Enable != nil {
			enable = *c.Enable
		}
		out = append(out, Client{
			Email: c.Email, UUID: uuid, TotalBytes: c.TotalGB, ExpiresAt: c.ExpiryTime,
			LimitIP: c.LimitIP, Enable: enable, Group: c.Group, SubID: c.SubID,
		})
	}
	return out
}

func ensureClient(s *Snapshot, c Client) {
	for i := range s.Clients {
		if s.Clients[i].Email != c.Email {
			continue
		}
		if s.Clients[i].UUID == "" {
			s.Clients[i].UUID = c.UUID
		}
		if s.Clients[i].SubID == "" {
			s.Clients[i].SubID = c.SubID
		}
		if s.Clients[i].Group == "" {
			s.Clients[i].Group = c.Group
		}
		return
	}
	s.Clients = append(s.Clients, c)
}

func addLink(s *Snapshot, email string, inboundID int) {
	key := email + "\x00" + strconv.Itoa(inboundID)
	if s.seenLinks[key] {
		return
	}
	s.seenLinks[key] = true
	s.Links = append(s.Links, Link{Email: email, InboundID: inboundID})
}

func readNodes(db *sql.DB, s *Snapshot, tables map[string]bool) error {
	if !tables["nodes"] {
		return nil // a panel without node support
	}
	cols := columns(db, "nodes")
	if !cols["id"] || !cols["name"] {
		return nil
	}
	rows, err := db.QueryContext(context.Background(), `SELECT id, name FROM nodes`)
	if err != nil {
		return nil
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

// readGroups collects group names and membership. This fork stores a client's
// group on its row (clients.group_name); upstream has no groups at all.
func readGroups(db *sql.DB, s *Snapshot, tables map[string]bool) {
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			s.Groups = append(s.Groups, name)
		}
	}
	if tables["client_groups"] {
		cols := columns(db, "client_groups")
		if cols["name"] {
			rows, err := db.QueryContext(context.Background(), `SELECT name FROM client_groups`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var name string
					if rows.Scan(&name) == nil {
						add(name)
					}
				}
				if err := rows.Err(); err != nil {
					return
				}
			}
		}
	}
	for _, c := range s.Clients {
		add(c.Group)
	}
	slices.Sort(s.Groups)
	for _, c := range s.Clients {
		if c.Group == "" {
			continue
		}
		s.GroupMembers[c.Group] = append(s.GroupMembers[c.Group], c.Email)
	}
	for name, emails := range s.GroupMembers {
		s.GroupMembers[name] = dedupeStrings(emails)
	}
}

func count(db *sql.DB, table string) int {
	var n int
	if db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n) != nil {
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
	// DefaultHost is the public name for an inbound whose settings carry no
	// Host header and whose share address is the node itself.
	DefaultHost string
	// Hosts overrides DefaultHost per node; the cutover names each node
	// (or a common country entry) explicitly.
	Hosts map[string]string
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
	// Reserved marks an inbound the sales bot must not add to new
	// subscriptions (the old panel's "nfc_" convention).
	Reserved bool
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
	SubID      string `json:"subId,omitempty"`
}

// Skip is an entity that does not map, with the reason.
type Skip struct{ Entity, Reason string }

// Report is the dry-run result.
type Report struct {
	Schema       *Schema             `json:"schema,omitempty"`
	Inbounds     []PlannedInbound    `json:"inbounds"`
	Clients      []PlannedClient     `json:"clients"`
	Groups       []string            `json:"groups"`
	GroupMembers map[string][]string `json:"groupMembers,omitempty"`
	// Aliases maps an email to the old panel's subscription id; the id is
	// kept as a native alias so old URLs keep working.
	Aliases map[string]string `json:"aliases,omitempty"`
	Skipped []Skip            `json:"skipped"`
	Notes   []string          `json:"notes"`
}

var (
	validTag  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	validUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Plan maps the snapshot onto native records.
func Plan(s *Snapshot, opt Options) Report {
	var r Report
	schema := s.Schema
	r.Schema = &schema
	r.Groups = append(r.Groups, s.Groups...)
	r.GroupMembers = map[string][]string{}
	for name, emails := range s.GroupMembers {
		r.GroupMembers[name] = append([]string(nil), emails...)
	}
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
	usedTag := map[string]bool{}

	for _, in := range s.Inbounds {
		clientCount := len(byInbound[in.ID])
		if !in.Enable {
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q", in.Tag), "disabled in the old panel"})
			continue
		}
		if strings.ToLower(in.Protocol) != "vless" {
			reason := "the native node model runs VLESS only; port it as a sidecar later"
			if clientCount > 0 {
				reason += fmt.Sprintf(" (%d client(s) stay in the old panel)", clientCount)
			}
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q (%s)", in.Tag, in.Protocol), reason})
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
			reason := fmt.Sprintf("transport %q has no native equivalent yet", network)
			if clientCount > 0 {
				reason += fmt.Sprintf(" (%d client(s) stay in the old panel)", clientCount)
			}
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q", in.Tag), reason})
			continue
		}
		tag := nativeTag(in)
		if !validTag.MatchString(tag) {
			r.Skipped = append(r.Skipped, Skip{fmt.Sprintf("inbound %q", in.Tag), "the tag cannot be used as a native inbound tag"})
			continue
		}
		tagKey := node + "/" + tag
		if usedTag[tagKey] {
			r.Skipped = append(r.Skipped, Skip{
				fmt.Sprintf("inbound %q", in.Tag),
				fmt.Sprintf("tag %q is already used by another imported inbound on node %s", tag, node),
			})
			continue
		}
		usedTag[tagKey] = true
		host := opt.Hosts[node]
		if host == "" {
			host = in.ShareAddr
			if host == "" || strings.EqualFold(in.ShareMode, "node") {
				host = hostHeader
			}
			if host == "" {
				host = opt.DefaultHost
			}
			if host == "" {
				host = node
			}
		}
		pi := PlannedInbound{
			OldID: in.ID, Node: node, Tag: tag, Port: in.Port,
			Network: network, Path: path, Host: host, Remarks: []string{in.Remark},
			Reserved: isReservedRemark(in.Remark),
		}
		r.Inbounds = append(r.Inbounds, pi)

		for _, email := range byInbound[in.ID] {
			c, ok := clients[email]
			if !ok {
				r.Skipped = append(r.Skipped, Skip{"client " + email, "attached to an inbound but missing from the old panel's client data"})
				continue
			}
			if !validUUID.MatchString(strings.ToLower(c.UUID)) {
				r.Skipped = append(r.Skipped, Skip{
					"client " + email,
					"the old record has no VLESS UUID (a protocol-specific credential, not portable)",
				})
				continue
			}
			r.Clients = append(r.Clients, PlannedClient{
				Node: node, Inbound: tag, Email: c.Email, UUID: c.UUID, Enable: c.Enable,
				TotalBytes: c.TotalBytes, ExpiresAt: c.ExpiresAt, LimitIP: c.LimitIP, SubID: c.SubID,
			})
		}
	}

	r.Aliases = map[string]string{}
	for _, c := range r.Clients {
		if c.SubID == "" {
			continue
		}
		if prev, ok := r.Aliases[c.Email]; ok {
			if prev != c.SubID {
				r.Notes = append(r.Notes, fmt.Sprintf("email %s has several old subscription ids; the first one is preserved", c.Email))
			}
			continue
		}
		r.Aliases[c.Email] = c.SubID
	}
	if len(r.Aliases) > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d old subscription id(s) become aliases of the new subscriptions", len(r.Aliases)))
	}

	// clients with no link row and no settings entry: report them rather than
	// lose them
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
			"%d client(s) have no inbound link in client_inbounds or in settings JSON; they must be linked by hand",
			unattached))
	}
	switch s.Schema.Style {
	case "upstream-settings":
		r.Notes = append(r.Notes, fmt.Sprintf(
			"%d client(s) were read from inbounds.settings (upstream shape)", s.Schema.SettingsClients))
	case "mixed":
		r.Notes = append(r.Notes, fmt.Sprintf(
			"clients exist both in the clients table and in inbounds.settings; the table wins for a duplicate email (%d settings entries)",
			s.Schema.SettingsClients))
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

// Redacted masks the identifiers that are secrets in a plan: each client's
// UUID and its old subscription id.
func (r Report) Redacted() Report {
	out := r
	out.Clients = append([]PlannedClient(nil), r.Clients...)
	for i := range out.Clients {
		out.Clients[i].UUID = Mask(out.Clients[i].UUID)
		out.Clients[i].SubID = Mask(out.Clients[i].SubID)
	}
	if r.Aliases != nil {
		out.Aliases = make(map[string]string, len(r.Aliases))
		for email, token := range r.Aliases {
			out.Aliases[email] = Mask(token)
		}
	}
	return out
}

// Mask keeps a short prefix and suffix so two records can still be told apart
// in a ticket without disclosing the value.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return "***"
	}
	return v[:4] + "..." + v[len(v)-4:]
}

// Markdown renders the plan as a report an operator can paste into a ticket.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Import plan\n\n")
	fmt.Fprintf(&b, "- inbounds: %d\n- client records: %d\n- groups: %d\n- skipped: %d\n\n",
		len(r.Inbounds), len(r.Clients), len(r.Groups), len(r.Skipped))
	if r.Schema != nil {
		b.WriteString("## Detected schema\n\n")
		fmt.Fprintf(&b, "- style: `%s`\n- tables: %s\n- inbounds: %d, client rows: %d, client_inbounds rows: %d, settings clients: %d\n\n",
			r.Schema.Style, strings.Join(r.Schema.Tables, ", "), r.Schema.Inbounds,
			r.Schema.Clients, r.Schema.ClientInbounds, r.Schema.SettingsClients)
	}
	if len(r.Inbounds) > 0 {
		b.WriteString("## Inbounds\n\n| Node | Tag | Port | Network | Path | Host |\n|---|---|---|---|---|---|\n")
		for _, in := range r.Inbounds {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s |\n",
				md(in.Node), md(in.Tag), in.Port, md(in.Network), md(in.Path), md(in.Host))
		}
		b.WriteString("\n")
	}
	if len(r.Clients) > 0 {
		b.WriteString("## Clients\n\n| Node | Inbound | Email | UUID | Enabled | Limit | Expiry |\n|---|---|---|---|---|---|---|\n")
		for _, c := range r.Clients {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %t | %d | %d |\n",
				md(c.Node), md(c.Inbound), md(c.Email), md(c.UUID), c.Enable, c.LimitIP, c.ExpiresAt)
		}
		b.WriteString("\n")
	}
	if len(r.Groups) > 0 {
		b.WriteString("## Groups\n\n")
		for _, g := range r.Groups {
			members := r.GroupMembers[g]
			if len(members) == 0 {
				fmt.Fprintf(&b, "- %s\n", g)
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", g, strings.Join(members, ", "))
		}
		b.WriteString("\n")
	}
	if len(r.Skipped) > 0 {
		b.WriteString("## Skipped\n\n")
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "- %s — %s\n", s.Entity, s.Reason)
		}
		b.WriteString("\n")
	}
	if len(r.Notes) > 0 {
		b.WriteString("## Notes\n\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}

func md(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

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

// isReservedRemark reports whether an old remark marks an inbound the sales
// bot must not provision onto (the old panel's "nfc_" convention).
func isReservedRemark(remark string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(remark)), "nfc_")
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
