package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
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
	db := fs.String("db", "", "path to the old panel's SQLite (x-ui.db)")
	localNode := fs.String("local-node", "", "native node that takes the old panel's own inbounds")
	nodeMap := fs.String("node-map", "", "rename old node ids: 2=ru4,3=de2")
	jsonOut := fs.Bool("json", false, "print the plan as JSON")
	apply := fs.Bool("apply", false, "send the plan to the master instead of only printing it")
	masterURL := fs.String("master", "", "master admin URL (with -apply), e.g. http://127.0.0.1:8080")
	token := fs.String("token", "", "admin API token (with -apply)")
	insecure := fs.Bool("insecure", false, "skip TLS verification of the master")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return fmt.Errorf("-db is required")
	}
	snap, err := importpanel.Read(*db)
	if err != nil {
		return err
	}
	rep := importpanel.Plan(snap, importpanel.Options{
		LocalNode: *localNode,
		NodeMap:   parseNodeMap(*nodeMap),
	})
	if *jsonOut || !*apply {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(b))
		if !*apply {
			fmt.Fprintf(stderr, "\n%d inbound(s), %d client record(s), %d group(s), %d skipped — run again with -apply to import\n",
				len(rep.Inbounds), len(rep.Clients), len(rep.Groups), len(rep.Skipped))
			return nil
		}
	}
	if *masterURL == "" || *token == "" {
		return fmt.Errorf("-apply needs -master and -token")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
	}
	api := &adminClient{base: strings.TrimSuffix(*masterURL, "/"), token: *token, http: client, log: log.New(stderr, "import ", 0)}

	created, existed := 0, 0
	for _, in := range rep.Inbounds {
		code, err := api.do("POST", "/inbounds/"+in.Node, map[string]any{
			"tag": in.Tag, "port": in.Port, "network": in.Network, "path": in.Path,
			"host": in.Host, "route": "direct",
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
		code, err := api.do("POST", "/groups", map[string]any{"name": g})
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
	fmt.Fprintf(stdout, "imported: %d inbound(s) (%d existed), %d client record(s) (%d existed), %d group(s)\n",
		created, existed, clientsCreated, clientsExisted, groups)
	for _, n := range rep.Notes {
		fmt.Fprintln(stdout, "note:", n)
	}
	for _, s := range rep.Skipped {
		fmt.Fprintf(stdout, "skipped: %s — %s\n", s.Entity, s.Reason)
	}
	return nil
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
	req, err := http.NewRequest(method, a.base+path, bytes.NewReader(raw))
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
