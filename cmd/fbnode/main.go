// Command fbnode is the node agent: Xray plus a loop that binds to the
// nearest master, pulls the node's desired state and reports traffic.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/enroll"
	"github.com/mhsanaei/3x-ui/v3/internal/native/failban"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vdns"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

type duration struct{ time.Duration }

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	d.Duration = v
	return err
}

type config struct {
	Node    string            `json:"node"`
	Masters map[string]string `json:"masters"` // name -> tls://host:port
	CA      string            `json:"ca"`
	Cert    string            `json:"cert"`
	Key     string            `json:"key"`
	DataDir string            `json:"dataDir"`
	Xray    struct {
		Bin    string `json:"bin"`
		Config string `json:"config"`
	} `json:"xray"`
	// Listen is the edge address (default ":443").
	Listen      string   `json:"listen"`
	RebindEvery duration `json:"rebindEvery"`
	MinPacket   int      `json:"minPacket"`
	ReportEvery duration `json:"reportEvery"`
	PullEvery   duration `json:"pullEvery"`
	BanEvery    duration `json:"banEvery"`
	BanHold     duration `json:"banHold"`
	Heartbeat   duration `json:"heartbeat"`
}

func load(path string) (*config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &config{DataDir: "/var/lib/fbnode", Listen: ":443"}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Node == "" || len(c.Masters) == 0 || c.Xray.Bin == "" {
		return nil, errors.New("node, masters and xray.bin are required")
	}
	if c.Xray.Config == "" {
		c.Xray.Config = filepath.Join(c.DataDir, "xray.json")
	}
	return c, nil
}

func clientTLS(c *config) (*tls.Config, error) {
	caPEM, err := os.ReadFile(c.CA)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no CA certificate in " + c.CA)
	}
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert}}, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		if err := runEnroll(os.Args[2:], os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "fbnode enroll:", err)
			os.Exit(1)
		}
		return
	}
	path := flag.String("c", "/etc/fbnode/config.json", "config file")
	flag.Parse()
	logger := log.New(os.Stderr, "fbnode ", log.LstdFlags)
	if err := run(*path, logger); err != nil {
		logger.Fatal(err)
	}
}

// runEnroll redeems an enrollment token and writes the node's identity and a
// config that connects to the given masters.
func runEnroll(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	master := fs.String("master", "", "master URL that mints the certificate (https://master.<zone> or http://127.0.0.1:8080)")
	token := fs.String("token", "", "one-time enrollment token from POST /enroll/tokens")
	node := fs.String("node", "", "this node's name")
	dir := fs.String("dir", "/etc/fbnode", "where to write ca.pem, cert.pem, key.pem and config.json")
	masters := fs.String("masters", "", "master name=URL pairs, comma separated (tls://<name>.m.<zone>:443)")
	xrayBin := fs.String("xray", "/usr/local/x-ui/bin/xray-linux-amd64", "xray binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *master == "" || *token == "" || *node == "" || *masters == "" {
		return errors.New("-master, -token, -node and -masters are required")
	}
	res, err := enroll.Exchange(context.Background(), *master, *token, *node, nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	for name, body := range map[string]string{"ca.pem": res.CA, "cert.pem": res.Cert, "key.pem": res.Key} {
		if err := os.WriteFile(filepath.Join(*dir, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	targets := map[string]string{}
	for _, pair := range strings.Split(*masters, ",") {
		name, url, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || name == "" || url == "" {
			return fmt.Errorf("bad -masters entry %q (name=url)", pair)
		}
		targets[name] = url
	}
	cfg := map[string]any{
		"node": *node, "masters": targets,
		"ca":      filepath.Join(*dir, "ca.pem"),
		"cert":    filepath.Join(*dir, "cert.pem"),
		"key":     filepath.Join(*dir, "key.pem"),
		"dataDir": filepath.Join(*dir, "data"),
		"xray":    map[string]string{"bin": *xrayBin},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, "config.json"), raw, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "enrolled %s; wrote %s/config.json\n", *node, *dir)
	return nil
}

func run(path string, logger *log.Logger) error {
	c, err := load(path)
	if err != nil {
		return err
	}
	tlsCfg, err := clientTLS(c)
	if err != nil {
		return err
	}
	masters := make([]string, 0, len(c.Masters))
	for m := range c.Masters {
		masters = append(masters, m)
	}
	slices.Sort(masters)
	inbox := wire.NodeInbox(c.Node)

	xr := &xrayrun.Runner{Bin: c.Xray.Bin, Config: c.Xray.Config, Output: logger.Writer()}
	defer xr.Stop()
	edge := &edgerun.Runner{Listen: c.Listen, Logf: logger.Printf}
	defer edge.Close()
	pres := &presence.Map{}
	dns := vdns.NewManager(logger.Printf)
	defer dns.Close()
	bundles := &agent.BundleApplier{Xray: xr, Edge: edge, DNS: dns, Node: c.Node, Presence: pres}
	app, err := desired.OpenApplier(filepath.Join(c.DataDir, "state"), func(ctx context.Context, st desired.State) error {
		logger.Printf("applying bundle %.12s (stamp %s)", st.Hash, st.Stamp)
		return bundles.Apply(ctx, st)
	})
	if err != nil {
		return err
	}
	journal, err := agent.OpenJournal(filepath.Join(c.DataDir, "journal"), c.Node)
	if err != nil {
		return err
	}
	defer journal.Close()
	prober := probe.NATS(probe.Config{URLs: c.Masters, TLS: tlsCfg, Inbox: inbox})
	sel := pick.New(masters, prober, pick.Settings{RebindEvery: c.RebindEvery.Duration, MinPacket: c.MinPacket})
	node := &agent.Node{
		Name: c.Node, Applier: app, Xray: xr, Log: logger.Printf, DrainState: edge.DrainState,
		Health: reports.HealthOf, Version: version,
		ReportEvery: c.ReportEvery.Duration, PullEvery: c.PullEvery.Duration, Heartbeat: c.Heartbeat.Duration,
		Sender: &agent.Sender{
			Journal: journal, Select: sel,
			Dial: func(_ context.Context, m string) (*nats.Conn, error) {
				return nats.Connect(c.Masters[m], nats.Secure(tlsCfg), nats.TLSHandshakeFirst(), nats.CustomInboxPrefix(inbox),
					nats.Name(c.Node), nats.MaxReconnects(-1))
			},
		},
	}
	ipLimit := &agent.IPLimit{
		Xray: xr, Applier: bundles, Limiter: &failban.Limiter{Hold: c.BanHold.Duration},
		Every: c.BanEvery.Duration, Log: logger.Printf,
	}
	pr := agent.NewPresence(c.Node, xr, pres, bundles, func(ctx context.Context) (*nats.Conn, error) {
		m := sel.Current()
		if m == "" {
			return nil, errors.New("no master yet")
		}
		return node.Sender.Conn(ctx, m)
	}, logger.Printf)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := node.Start(ctx); err != nil {
		logger.Printf("restoring the last state: %v", err)
	}
	logger.Printf("node %s, masters %v", c.Node, masters)
	go pr.Run(ctx)
	go ipLimit.Run(ctx)
	node.Run(ctx)
	return nil
}
