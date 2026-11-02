// Package bus runs a master's embedded NATS JetStream: its own op stream plus
// mirrors of every other master's, joined by gateways so each side of a split keeps writing.
package bus

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

// Config places one master in the supercluster: a one-server cluster named
// after the master, gateways to the others.
type Config struct {
	Name     string
	StoreDir string
	// Host/Port: client listener (agents, tools); Port -1 picks a free one.
	Host string
	Port int
	// RoutePort is only needed because JetStream insists on a cluster.
	RoutePort   int
	GatewayHost string
	GatewayPort int
	// Advertise is how peers reach this gateway; it must not be a local-only address in production.
	Advertise string
	Peers     map[string]string // master name -> gateway URL
	// TLS, when set, is mutual TLS on the gateways (fleetca.MutualTLS).
	TLS *tls.Config
	// ClientTLS, when set, makes the client port require a fleet certificate
	// whose CN is "node.<name>"; that node may only send its own reports.
	ClientTLS *tls.Config
	Logger    server.Logger
}

type Bus struct {
	name  string
	srv   *server.Server
	nc    *nats.Conn
	js    jetstream.JetStream
	probe *nats.Subscription
	// clustered: joined to other masters, streams are placed on this one
	clustered bool
}

func Start(cfg Config) (*Bus, error) {
	if cfg.Name == "" || cfg.StoreDir == "" {
		return nil, errors.New("bus: name and store dir are required")
	}
	o := &server.Options{
		ServerName: cfg.Name, Host: cfg.Host, Port: cfg.Port,
		JetStream: true, StoreDir: cfg.StoreDir, NoSigs: true,
	}
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	// a master without peers runs JetStream standalone: a cluster of one never forms a quorum
	if len(cfg.Peers) > 0 {
		o.Cluster.Name, o.Cluster.Host, o.Cluster.Port = cfg.Name, "127.0.0.1", cfg.RoutePort
		self, err := url.Parse("nats://127.0.0.1:" + strconv.Itoa(cfg.RoutePort))
		if err != nil {
			return nil, err
		}
		o.Routes = []*url.URL{self}
		o.Gateway.Name, o.Gateway.Host, o.Gateway.Port = cfg.Name, cfg.GatewayHost, cfg.GatewayPort
		o.Gateway.Advertise, o.Gateway.ConnectRetries = cfg.Advertise, -1
		if cfg.TLS != nil {
			o.Gateway.TLSConfig, o.Gateway.TLSTimeout = cfg.TLS, 5
		}
		for name, raw := range cfg.Peers {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("bus: peer %s: %w", name, err)
			}
			remote := &server.RemoteGatewayOpts{Name: name, URLs: []*url.URL{u}}
			if cfg.TLS != nil {
				remote.TLSConfig, remote.TLSTimeout = cfg.TLS.Clone(), 5
			}
			o.Gateway.Gateways = append(o.Gateway.Gateways, remote)
		}
	}
	var token string
	if cfg.ClientTLS != nil {
		token = rand.Text()
		o.TLSConfig, o.TLSVerify, o.TLSTimeout = cfg.ClientTLS, true, 5
		// TLS before NATS' INFO, so an edge can route nodes to it by SNI on 443
		o.TLSHandshakeFirst = true
		o.CustomClientAuthentication = nodeAuth{token: token}
	}
	srv, err := server.NewServer(o)
	if err != nil {
		return nil, err
	}
	if cfg.Logger != nil {
		srv.SetLogger(cfg.Logger, false, false)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, errors.New("bus: server did not start")
	}
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.MaxReconnects(-1), nats.Token(token))
	if err != nil {
		srv.Shutdown()
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return nil, err
	}
	return &Bus{name: cfg.Name, srv: srv, nc: nc, js: js, clustered: len(cfg.Peers) > 0}, nil
}

func opsStream(origin string) string          { return "OPS_" + origin }
func mirrorStream(self, origin string) string { return "M_" + self + "_" + origin }
func opsSubject(origin string) string         { return "ops." + origin }
func repStream(master string) string          { return "REP_" + master }
func repMirror(self, master string) string    { return "MR_" + self + "_" + master }

// nodeAuth lets in the master's own connection by token and nodes by the
// verified fleet certificate, each limited to its own subjects.
type nodeAuth struct{ token string }

func (a nodeAuth) Check(c server.ClientAuthentication) bool {
	if c.Kind() != server.CLIENT {
		return false
	}
	if opts := c.GetOpts(); opts != nil && a.token != "" && opts.Token == a.token {
		c.RegisterUser(&server.User{Username: "master"})
		return true
	}
	cs := c.GetTLSConnectionState()
	if cs == nil || len(cs.VerifiedChains) == 0 {
		return false
	}
	node, ok := strings.CutPrefix(cs.VerifiedChains[0][0].Subject.CommonName, "node.")
	if !ok || node == "" || strings.ContainsAny(node, ".*> ") {
		return false
	}
	c.RegisterUser(&server.User{Username: wire.NodeCN(node), Permissions: &server.Permissions{
		Publish:   &server.SubjectPermission{Allow: []string{"rep.*." + node, "probe.*", wire.DesiredSubject("*", node), "presence." + node}},
		Subscribe: &server.SubjectPermission{Allow: []string{wire.NodeInbox(node) + ".>", "presence.*"}},
	}})
	return true
}

// streamFor is where this master reads an origin's ops.
func (b *Bus) streamFor(origin string) string {
	if origin == b.name {
		return opsStream(origin)
	}
	return mirrorStream(b.name, origin)
}

func (b *Bus) repStreamFor(master string) string {
	if master == b.name {
		return repStream(master)
	}
	return repMirror(b.name, master)
}

// Ensure creates this master's op and report streams and a mirror of every
// other master's. It needs the JetStream meta quorum, so it runs while joined.
func (b *Bus) Ensure(ctx context.Context, masters []string) error {
	var place *jetstream.Placement
	if b.clustered {
		place = &jetstream.Placement{Cluster: b.name}
	}
	own := []jetstream.StreamConfig{{
		Name: opsStream(b.name), Subjects: []string{opsSubject(b.name)},
		Storage: jetstream.FileStorage, Placement: place, AllowDirect: true,
		Duplicates: 10 * time.Minute,
	}, {
		Name: repStream(b.name), Subjects: []string{wire.ReportSubject(b.name, "*")},
		Storage: jetstream.FileStorage, Placement: place, AllowDirect: true,
		Duplicates: 10 * time.Minute, MaxAge: wire.ReportRetention,
	}}
	for _, m := range masters {
		if m == b.name {
			continue
		}
		own = append(own, jetstream.StreamConfig{
			Name: mirrorStream(b.name, m), Storage: jetstream.FileStorage, Placement: place,
			AllowDirect: true, Mirror: &jetstream.StreamSource{Name: opsStream(m)},
		}, jetstream.StreamConfig{
			Name: repMirror(b.name, m), Storage: jetstream.FileStorage, Placement: place,
			AllowDirect: true, MaxAge: wire.ReportRetention, Mirror: &jetstream.StreamSource{Name: repStream(m)},
		})
	}
	for _, cfg := range own {
		if _, err := b.js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("bus: stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// ReportRange is the first and last report seq held locally for a master's
// stream; first moves up as old reports age out.
func (b *Bus) ReportRange(ctx context.Context, master string) (first, last uint64, err error) {
	st, err := b.js.Stream(ctx, b.repStreamFor(master))
	if err != nil {
		return 0, 0, err
	}
	state := st.CachedInfo().State
	return state.FirstSeq, state.LastSeq, nil
}

// GetReport reads one raw report of a master's stream by seq.
func (b *Bus) GetReport(ctx context.Context, master string, seq uint64) ([]byte, error) {
	st, err := b.js.Stream(ctx, b.repStreamFor(master))
	if err != nil {
		return nil, err
	}
	msg, err := st.GetMsg(ctx, seq)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// Publish appends a local op; its seq must be the stream's next, so two
// writers on one master cannot interleave out of order.
func (b *Bus) Publish(ctx context.Context, op oplog.Op) error {
	if op.Origin != b.name {
		return fmt.Errorf("bus: %s cannot publish ops of %s", b.name, op.Origin)
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	ack, err := b.js.Publish(ctx, opsSubject(b.name), data,
		jetstream.WithExpectLastSequence(op.Seq-1),
		jetstream.WithMsgID(op.Origin+":"+strconv.FormatUint(op.Seq, 10)))
	if err != nil {
		return err
	}
	if ack.Sequence != op.Seq {
		return fmt.Errorf("bus: op %d stored as %d", op.Seq, ack.Sequence)
	}
	return nil
}

// Get reads one op of an origin by seq from the local copy.
func (b *Bus) Get(ctx context.Context, origin string, seq uint64) (oplog.Op, error) {
	st, err := b.js.Stream(ctx, b.streamFor(origin))
	if err != nil {
		return oplog.Op{}, err
	}
	msg, err := st.GetMsg(ctx, seq)
	if err != nil {
		return oplog.Op{}, err
	}
	var op oplog.Op
	if err := json.Unmarshal(msg.Data, &op); err != nil {
		return oplog.Op{}, err
	}
	if op.Origin != origin || op.Seq != seq {
		return oplog.Op{}, fmt.Errorf("bus: %s#%d holds %s#%d", origin, seq, op.Origin, op.Seq)
	}
	return op, nil
}

// Heads is the last seq held locally for every master.
func (b *Bus) Heads(ctx context.Context, masters []string) (oplog.Vector, error) {
	v := oplog.Vector{}
	for _, m := range masters {
		st, err := b.js.Stream(ctx, b.streamFor(m))
		if err != nil {
			return nil, fmt.Errorf("bus: %s: %w", m, err)
		}
		if last := st.CachedInfo().State.LastSeq; last > 0 {
			v[m] = last
		}
	}
	return v, nil
}

// ServeProbe answers node probes with load() (0 idle, 1 saturated).
func (b *Bus) ServeProbe(load func() float64) error {
	sub, err := b.nc.Subscribe(wire.ProbeSubject(b.name), func(m *nats.Msg) {
		n, _ := strconv.Atoi(string(m.Data))
		n = min(max(n, 0), wire.MaxProbeBytes)
		reply := nats.NewMsg(m.Reply)
		reply.Header.Set("Load", strconv.FormatFloat(load(), 'f', 3, 64))
		reply.Data = make([]byte, n)
		_ = m.RespondMsg(reply)
	})
	if err != nil {
		return err
	}
	b.probe = sub
	return b.nc.Flush()
}

// Conn is the master's own connection, for services that answer nodes.
func (b *Bus) Conn() *nats.Conn { return b.nc }

// ClientURL is where agents connect.
func (b *Bus) ClientURL() string { return b.srv.ClientURL() }

// InboundGateways names the masters connected to this one.
func (b *Bus) InboundGateways() []string {
	gz, err := b.srv.Gatewayz(nil)
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(gz.InboundGateways))
}

// GatewayURL is where peers dial this master's gateway.
func (b *Bus) GatewayURL() string {
	return fmt.Sprintf("nats://127.0.0.1:%d", b.srv.GatewayAddr().Port)
}

func (b *Bus) Close() {
	b.nc.Close()
	b.srv.Shutdown()
	b.srv.WaitForShutdown()
}
