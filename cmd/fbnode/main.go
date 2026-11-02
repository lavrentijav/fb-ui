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
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/edgerun"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

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
	path := flag.String("c", "/etc/fbnode/config.json", "config file")
	flag.Parse()
	logger := log.New(os.Stderr, "fbnode ", log.LstdFlags)
	if err := run(*path, logger); err != nil {
		logger.Fatal(err)
	}
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

	xr := &xrayrun.Runner{Bin: c.Xray.Bin, Config: c.Xray.Config}
	defer xr.Stop()
	edge := &edgerun.Runner{Listen: c.Listen, Logf: logger.Printf}
	defer edge.Close()
	pres := &presence.Map{}
	bundles := &agent.BundleApplier{Xray: xr, Edge: edge, Node: c.Node, Presence: pres}
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
		ReportEvery: c.ReportEvery.Duration, PullEvery: c.PullEvery.Duration,
		Sender: &agent.Sender{
			Journal: journal, Select: sel,
			Dial: func(_ context.Context, m string) (*nats.Conn, error) {
				return nats.Connect(c.Masters[m], nats.Secure(tlsCfg), nats.TLSHandshakeFirst(), nats.CustomInboxPrefix(inbox),
					nats.Name(c.Node), nats.MaxReconnects(-1))
			},
		},
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
	node.Run(ctx)
	return nil
}
