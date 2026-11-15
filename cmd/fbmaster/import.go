package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/importpanel"
)

// runImport reads the old panel's database and, with -apply, sends the plan to
// a running master. Without -apply it only prints what would happen.
func runImport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "path to the old panel's SQLite (x-ui.db), opened read-only")
	localNode := fs.String("local-node", "", "native node that takes the old panel's own inbounds")
	nodeMap := fs.String("node-map", "", "rename old node ids: 2=ru4,3=de2")
	shareHost := fs.String("share-host", "", "public host for inbounds whose settings carry no Host header; accepts node=host pairs")
	jsonOut := fs.Bool("json", false, "print the plan as JSON (same as -report json)")
	reportFormat := fs.String("report", "json", "report format: json or markdown")
	schemaOnly := fs.Bool("schema", false, "print the detected database schema and exit")
	apply := fs.Bool("apply", false, "send the plan to the master instead of only printing it")
	verify := fs.Bool("verify", false, "compare the plan and the applied records with the master")
	redact := fs.Bool("redact", true, "mask UUIDs and subscription ids in the report")
	withSecrets := fs.Bool("with-secrets", false, "print raw identifiers (overrides -redact)")
	masterURL := fs.String("master", "", "master admin URL (with -apply/-verify), e.g. http://127.0.0.1:8080")
	token := fs.String("token", "", "admin API token (with -apply/-verify)")
	insecure := fs.Bool("insecure", false, "skip TLS verification of the master")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return fmt.Errorf("-db is required")
	}
	if *jsonOut {
		*reportFormat = "json"
	}
	if *reportFormat != "json" && *reportFormat != "markdown" {
		return fmt.Errorf("-report must be json or markdown, not %q", *reportFormat)
	}
	if *withSecrets {
		*redact = false
	}

	snap, err := importpanel.Read(*db)
	if err != nil {
		return err
	}
	if *schemaOnly {
		return writeSchema(stdout, *reportFormat, snap.Schema)
	}
	defaultHost, nodeHosts := parseShareHost(*shareHost)
	rep := importpanel.Plan(snap, importpanel.Options{
		LocalNode:   *localNode,
		NodeMap:     parseNodeMap(*nodeMap),
		DefaultHost: defaultHost,
		Hosts:       nodeHosts,
	})

	explicitReport := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "report" || f.Name == "json" {
			explicitReport = true
		}
	})
	shown := rep
	if *redact {
		shown = rep.Redacted()
	}
	if (!*apply && !*verify) || explicitReport {
		if err := writeReport(stdout, *reportFormat, shown); err != nil {
			return err
		}
		if !*apply && !*verify {
			fmt.Fprintf(stderr, "\n%d inbound(s), %d client record(s), %d group(s), %d skipped — run again with -apply to import\n",
				len(rep.Inbounds), len(rep.Clients), len(rep.Groups), len(rep.Skipped))
		}
	}
	if !*apply && !*verify {
		return nil
	}
	if *masterURL == "" || *token == "" {
		return fmt.Errorf("-apply and -verify need -master and -token")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
	}
	api := &adminClient{base: strings.TrimSuffix(*masterURL, "/"), token: *token, http: client, log: log.New(stderr, "import ", 0)}

	if *apply {
		if err := applyPlan(api, rep, stdout); err != nil {
			return err
		}
	}
	if *verify {
		res, err := verifyPlan(api, rep)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if err := writeVerify(stdout, *reportFormat, res); err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("verify found %d problem(s)", len(res.Problems))
		}
	}
	return nil
}

func writeReport(w io.Writer, format string, rep importpanel.Report) error {
	if format == "markdown" {
		_, err := io.WriteString(w, rep.Markdown())
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func writeSchema(w io.Writer, format string, s importpanel.Schema) error {
	if format == "markdown" {
		_, err := fmt.Fprintf(w, "# Detected schema\n\n- style: `%s`\n- tables: %s\n- rows: inbounds %d, clients %d, client_inbounds %d, nodes %d, groups %d\n- clients inside inbounds.settings: %d\n",
			s.Style, strings.Join(s.Tables, ", "), s.Inbounds, s.Clients, s.ClientInbounds, s.Nodes, s.Groups, s.SettingsClients)
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func applyPlan(api *adminClient, rep importpanel.Report, stdout io.Writer) error {
	created, existed := 0, 0
	ensured := map[string]bool{}
	for _, in := range rep.Inbounds {
		if !ensured[in.Node] {
			if err := ensureNodeSpec(api, in.Node); err != nil {
				return err
			}
			ensured[in.Node] = true
		}
		remark := ""
		if len(in.Remarks) > 0 {
			remark = in.Remarks[0]
		}
		code, err := api.do("POST", "/inbounds/"+url.PathEscape(in.Node), map[string]any{
			"tag": in.Tag, "port": in.Port, "network": in.Network, "path": in.Path,
			"host": in.Host, "route": "direct", "remark": remark,
		})
		switch {
		case err != nil:
			return fmt.Errorf("inbound %s/%s: %w", in.Node, in.Tag, err)
		case code == http.StatusCreated:
			created++
		case code == http.StatusConflict:
			existed++
			api.log.Printf("inbound %s/%s already exists, kept", in.Node, in.Tag)
		default:
			return fmt.Errorf("inbound %s/%s: HTTP %d", in.Node, in.Tag, code)
		}
		if in.Reserved {
			code, err := api.do("POST", "/api/v1/inbounds/"+url.PathEscape(in.Node)+"/"+url.PathEscape(in.Tag)+"/params", map[string]any{
				"params": map[string]string{"reserved": "true"},
			})
			if err != nil {
				return fmt.Errorf("reserve inbound %s/%s: %w", in.Node, in.Tag, err)
			}
			if code != http.StatusOK && code != http.StatusCreated {
				return fmt.Errorf("reserve inbound %s/%s: HTTP %d", in.Node, in.Tag, code)
			}
			api.log.Printf("inbound %s/%s marked reserved (nfc_)", in.Node, in.Tag)
		}
	}
	clientsCreated, clientsExisted := 0, 0
	for _, c := range rep.Clients {
		code, err := api.do("PUT", "/clients", map[string]any{
			"node": c.Node, "inbound": c.Inbound, "email": c.Email, "uuid": c.UUID,
			"enable": c.Enable, "totalBytes": c.TotalBytes, "expiresAt": c.ExpiresAt, "limitIp": c.LimitIP,
		})
		switch {
		case err != nil:
			return fmt.Errorf("client %s: %w", c.Email, err)
		case code == http.StatusCreated:
			clientsCreated++
		case code == http.StatusConflict:
			clientsExisted++
			api.log.Printf("client %s on %s/%s already exists, kept", c.Email, c.Node, c.Inbound)
		default:
			return fmt.Errorf("client %s: HTTP %d", c.Email, code)
		}
	}
	groups := 0
	for _, g := range rep.Groups {
		body := map[string]any{"name": g}
		if emails := rep.GroupMembers[g]; len(emails) > 0 {
			body["emails"] = emails
		}
		code, err := api.do("POST", "/groups", body)
		if err != nil {
			return fmt.Errorf("group %s: %w", g, err)
		}
		switch code {
		case http.StatusCreated, http.StatusOK:
			groups++
		case http.StatusConflict:
			api.log.Printf("group %s already exists, kept", g)
		default:
			return fmt.Errorf("group %s: HTTP %d", g, code)
		}
	}
	aliases := 0
	for _, email := range sortedKeys(rep.Aliases) {
		var sub struct {
			Token string `json:"token"`
		}
		code, err := api.doJSON("PUT", "/sub/"+url.PathEscape(email), map[string]any{}, &sub)
		if err != nil {
			return fmt.Errorf("subscription %s: %w", email, err)
		}
		if (code != http.StatusOK && code != http.StatusCreated) || sub.Token == "" {
			return fmt.Errorf("subscription %s: HTTP %d", email, code)
		}
		code, err = api.do("POST", "/api/v1/subs/"+url.PathEscape(sub.Token)+"/aliases", map[string]any{
			"alias": rep.Aliases[email], "enable": true,
		})
		switch {
		case err != nil:
			return fmt.Errorf("alias %s: %w", email, err)
		case code == http.StatusOK || code == http.StatusCreated:
			aliases++
		case code == http.StatusConflict:
			api.log.Printf("alias for %s already exists, kept", email)
		default:
			return fmt.Errorf("alias %s: HTTP %d", email, code)
		}
	}
	fmt.Fprintf(stdout, "imported: %d inbound(s) (%d existed), %d client record(s) (%d existed), %d group(s), %d alias(es)\n",
		created, existed, clientsCreated, clientsExisted, groups, aliases)
	for _, n := range rep.Notes {
		fmt.Fprintln(stdout, "note:", n)
	}
	for _, s := range rep.Skipped {
		fmt.Fprintf(stdout, "skipped: %s — %s\n", s.Entity, s.Reason)
	}
	return nil
}

// ensureNodeSpec registers an empty spec for a node that has none. An
// existing spec is never replaced: it may hold inbounds, rules or certs.
func ensureNodeSpec(api *adminClient, node string) error {
	var inbounds []any
	code, err := api.get("/inbounds/"+url.PathEscape(node), &inbounds)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		code, err = api.do("PUT", "/nodes/"+url.PathEscape(node)+"/spec", map[string]any{"node": node})
		if err != nil {
			return fmt.Errorf("register node %s: %w", node, err)
		}
		if code != http.StatusOK {
			return fmt.Errorf("register node %s: HTTP %d", node, code)
		}
		api.log.Printf("registered an empty spec for %s", node)
		return nil
	default:
		return fmt.Errorf("GET /inbounds/%s: HTTP %d", node, code)
	}
}

// verifyCounts is one row of the verify report.
type verifyCounts struct {
	Planned    int `json:"planned"`
	Found      int `json:"found"`
	Missing    int `json:"missing"`
	Mismatched int `json:"mismatched,omitempty"`
}

type verifyResult struct {
	Inbounds      verifyCounts `json:"inbounds"`
	Clients       verifyCounts `json:"clients"`
	Groups        verifyCounts `json:"groups"`
	Subscriptions verifyCounts `json:"subscriptions"`
	Aliases       verifyCounts `json:"aliases"`
	Problems      []string     `json:"problems,omitempty"`
	Notes         []string     `json:"notes,omitempty"`
}

// OK reports whether every planned record is present and matches.
func (v verifyResult) OK() bool { return len(v.Problems) == 0 }

func (v verifyResult) Markdown() string {
	var b strings.Builder
	b.WriteString("# Import verification\n\n")
	fmt.Fprintf(&b, "| Section | Planned | Found | Missing | Mismatched |\n|---|---|---|---|---|\n")
	for _, row := range []struct {
		name string
		c    verifyCounts
	}{
		{"inbounds", v.Inbounds},
		{"clients", v.Clients},
		{"groups", v.Groups},
		{"subscriptions", v.Subscriptions},
		{"aliases", v.Aliases},
	} {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", row.name, row.c.Planned, row.c.Found, row.c.Missing, row.c.Mismatched)
	}
	if len(v.Problems) > 0 {
		b.WriteString("\n## Problems\n\n")
		for _, p := range v.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if len(v.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, n := range v.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}

func writeVerify(w io.Writer, format string, res verifyResult) error {
	if format == "markdown" {
		_, err := io.WriteString(w, res.Markdown())
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// verifyPlan compares every planned record with the master's current state.
// Links are checked through the subscription list where a subscription exists.
func verifyPlan(api *adminClient, rep importpanel.Report) (verifyResult, error) {
	var v verifyResult

	nodes := map[string]bool{}
	for _, in := range rep.Inbounds {
		nodes[in.Node] = true
	}
	actualInbounds := map[string]map[string]bool{}
	for _, node := range sortedKeys(nodes) {
		var list []struct {
			Tag string `json:"tag"`
		}
		code, err := api.get("/inbounds/"+url.PathEscape(node), &list)
		if err != nil {
			return v, err
		}
		switch code {
		case http.StatusOK:
			tags := map[string]bool{}
			for _, in := range list {
				tags[in.Tag] = true
			}
			actualInbounds[node] = tags
		case http.StatusNotFound:
			actualInbounds[node] = map[string]bool{}
		default:
			return v, fmt.Errorf("GET /inbounds/%s: HTTP %d", node, code)
		}
	}
	for _, in := range rep.Inbounds {
		v.Inbounds.Planned++
		if actualInbounds[in.Node][in.Tag] {
			v.Inbounds.Found++
			continue
		}
		v.Inbounds.Missing++
		v.Problems = append(v.Problems, fmt.Sprintf("inbound %s/%s is missing", in.Node, in.Tag))
	}

	var actualClients []struct {
		Node       string `json:"node"`
		Inbound    string `json:"inbound"`
		Email      string `json:"email"`
		UUID       string `json:"uuid"`
		Enable     bool   `json:"enable"`
		TotalBytes int64  `json:"totalBytes"`
		ExpiresAt  int64  `json:"expiresAt"`
		LimitIP    int    `json:"limitIp"`
	}
	if code, err := api.get("/clients", &actualClients); err != nil {
		return v, err
	} else if code != http.StatusOK {
		return v, fmt.Errorf("GET /clients: HTTP %d", code)
	}
	byKey := map[string]int{}
	for i, c := range actualClients {
		byKey[c.Node+"/"+c.Inbound+"/"+c.Email] = i
	}
	expectedLinks := map[string]int{}
	now := time.Now().UnixMilli()
	for _, c := range rep.Clients {
		v.Clients.Planned++
		if c.Enable && (c.ExpiresAt == 0 || now < c.ExpiresAt) {
			expectedLinks[c.Email]++
		}
		i, ok := byKey[c.Node+"/"+c.Inbound+"/"+c.Email]
		if !ok {
			v.Clients.Missing++
			v.Problems = append(v.Problems, fmt.Sprintf("client %s on %s/%s is missing", c.Email, c.Node, c.Inbound))
			continue
		}
		v.Clients.Found++
		got := actualClients[i]
		switch {
		case !strings.EqualFold(got.UUID, c.UUID):
			v.Clients.Mismatched++
			v.Problems = append(v.Problems, fmt.Sprintf("client %s on %s/%s has a different UUID", c.Email, c.Node, c.Inbound))
		case got.TotalBytes != c.TotalBytes || got.ExpiresAt != c.ExpiresAt || got.LimitIP != c.LimitIP || got.Enable != c.Enable:
			v.Clients.Mismatched++
			v.Problems = append(v.Problems, fmt.Sprintf("client %s on %s/%s has different quota/expiry/limits", c.Email, c.Node, c.Inbound))
		}
	}

	var actualGroups []struct {
		Name   string   `json:"name"`
		Emails []string `json:"emails"`
	}
	if code, err := api.get("/groups", &actualGroups); err != nil {
		return v, err
	} else if code != http.StatusOK {
		return v, fmt.Errorf("GET /groups: HTTP %d", code)
	}
	byGroup := map[string][]string{}
	for _, g := range actualGroups {
		byGroup[g.Name] = g.Emails
	}
	for _, g := range rep.Groups {
		v.Groups.Planned++
		emails, ok := byGroup[g]
		if !ok {
			v.Groups.Missing++
			v.Problems = append(v.Problems, fmt.Sprintf("group %s is missing", g))
			continue
		}
		v.Groups.Found++
		for _, want := range rep.GroupMembers[g] {
			if !contains(emails, want) {
				v.Groups.Mismatched++
				v.Problems = append(v.Problems, fmt.Sprintf("group %s does not contain %s", g, want))
			}
		}
	}

	var subs []struct {
		Token   string   `json:"token"`
		Email   string   `json:"email"`
		Links   int      `json:"links"`
		Aliases []string `json:"aliases"`
	}
	if code, err := api.get("/sub", &subs); err != nil {
		return v, err
	} else if code != http.StatusOK {
		return v, fmt.Errorf("GET /sub: HTTP %d", code)
	}
	linksOf := map[string]int{}
	aliasesOf := map[string][]string{}
	for _, s := range subs {
		linksOf[s.Email] = s.Links
		aliasesOf[s.Email] = append(aliasesOf[s.Email], s.Aliases...)
	}
	for _, email := range sortedKeys(rep.Aliases) {
		v.Aliases.Planned++
		if !contains(aliasesOf[email], rep.Aliases[email]) {
			v.Aliases.Missing++
			v.Problems = append(v.Problems, fmt.Sprintf("alias for %s is missing", email))
			continue
		}
		v.Aliases.Found++
	}
	for _, email := range sortedKeys(expectedLinks) {
		want := expectedLinks[email]
		v.Subscriptions.Planned++
		got, ok := linksOf[email]
		if !ok {
			v.Notes = append(v.Notes, fmt.Sprintf("no subscription for %s; links were not checked", email))
			continue
		}
		v.Subscriptions.Found++
		if got != want {
			v.Subscriptions.Mismatched++
			v.Problems = append(v.Problems, fmt.Sprintf("subscription %s has %d link(s), planned %d", email, got, want))
		}
	}
	return v, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseNodeMap reads "2=ru4,3=de2".
func parseNodeMap(v string) map[int]string {
	out := map[int]string{}
	for _, pair := range strings.Split(v, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, name, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil {
			continue
		}
		out[n] = strings.TrimSpace(name)
	}
	return out
}

// parseShareHost reads "host" or "node=host,node=host"; the bare value is
// the default, and an explicit node entry wins for that node.
func parseShareHost(v string) (string, map[string]string) {
	var def string
	hosts := map[string]string{}
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		node, host, ok := strings.Cut(item, "=")
		if !ok {
			if def == "" {
				def = item
			}
			continue
		}
		node, host = strings.TrimSpace(node), strings.TrimSpace(host)
		if node != "" && host != "" {
			hosts[node] = host
		}
	}
	if len(hosts) == 0 {
		return def, nil
	}
	return def, hosts
}

// adminClient talks to a master's authenticated admin surface.
type adminClient struct {
	base  string
	token string
	http  *http.Client
	log   *log.Logger
}

func (a *adminClient) do(method, path string, body any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, nil
}

// doJSON is do plus an optional decoded response, for calls whose answer the
// importer needs (the primary subscription token).
func (a *adminClient) doJSON(method, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s: %w", path, err)
		}
		return resp.StatusCode, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// get reads a JSON resource; a non-200 answer is returned to the caller.
func (a *adminClient) get(path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.base+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: %w", path, err)
	}
	return resp.StatusCode, nil
}
