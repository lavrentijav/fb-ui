package bus_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

func soloMaster(t *testing.T, ca *fleetca.CA) *bus.Bus {
	t.Helper()
	own, err := ca.Issue("ru2", []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bus.Start(bus.Config{
		Name: "ru2", StoreDir: t.TempDir(), Port: -1, RoutePort: bustest.FreePort(t),
		GatewayHost: "127.0.0.1", GatewayPort: bustest.FreePort(t), Logger: bustest.Quiet{},
		ClientTLS: ca.MutualTLS(own),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := b.Ensure(ctx, []string{"ru2"})
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err := b.ServeProbe(func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	return b
}

func connect(b *bus.Bus, cfg *tls.Config, inbox string) (*nats.Conn, error) {
	opts := []nats.Option{nats.Secure(cfg), nats.NoReconnect(), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {})}
	if inbox != "" {
		opts = append(opts, nats.CustomInboxPrefix(inbox))
	}
	return nats.Connect(b.ClientURL(), opts...)
}

// Only nodes with a fleet certificate get in, and a node can report only
// as itself; the master's own connection keeps full rights.
func TestNodesAuthenticateByFleetCert(t *testing.T) {
	ca, _ := fleetca.New("fleet")
	b := soloMaster(t, ca)
	publish(t, b, "ru2", 1, 1) // master's in-process connection still works

	nodeCert, _ := ca.Issue(bus.NodeCN("nl2"), nil, time.Hour)
	nodeTLS := ca.MutualTLS(nodeCert)
	nc, err := connect(b, nodeTLS, bus.NodeInbox("nl2"))
	if err != nil {
		t.Fatalf("node with fleet cert refused: %v", err)
	}
	defer nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := reports.Send(ctx, nc, "ru2", reports.Report{Node: "nl2", Seq: 1}); err != nil {
		t.Fatalf("own report: %v", err)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := reports.Send(short, nc, "ru2", reports.Report{Node: "de9", Seq: 1}); err == nil {
		t.Fatal("nl2 reported as de9")
	}
	prober := probe.NATS(probe.Config{URLs: map[string]string{"ru2": b.ClientURL()}, TLS: nodeTLS, Inbox: bus.NodeInbox("nl2")})
	if _, err := pick.New([]string{"ru2"}, prober, pick.Settings{}).Tick(context.Background()); err != nil {
		t.Fatalf("node probe: %v", err)
	}
	if _, last, _ := b.ReportRange(context.Background(), "ru2"); last != 1 {
		t.Fatalf("report stream holds %d, want only nl2's", last)
	}

	masterCert, _ := ca.Issue("de2", nil, time.Hour)
	foreign, _ := fleetca.New("other")
	foreignCert, _ := foreign.Issue(bus.NodeCN("nl2"), nil, time.Hour)
	for name, cfg := range map[string]*tls.Config{
		"master cert on the client port": ca.MutualTLS(masterCert),
		"foreign CA":                     func() *tls.Config { c := ca.MutualTLS(foreignCert); return c }(),
		"no cert":                        {RootCAs: nodeTLS.RootCAs, MinVersion: tls.VersionTLS13},
	} {
		if c, err := connect(b, cfg, ""); err == nil {
			c.Close()
			t.Fatalf("%s: connected", name)
		}
	}
}
