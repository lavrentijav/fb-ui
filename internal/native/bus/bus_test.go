package bus_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

func mkOp(origin string, seq uint64) oplog.Op {
	return oplog.Op{
		Origin: origin, Seq: seq, Stamp: hlc.Stamp{Wall: int64(seq), Origin: origin},
		Table: "clients", Key: "k", Fields: map[string]json.RawMessage{"n": json.RawMessage(fmt.Sprint(seq))},
	}
}

func publish(t *testing.T, b *bus.Bus, origin string, from, to uint64) {
	t.Helper()
	for s := from; s <= to; s++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := b.Publish(ctx, mkOp(origin, s))
		cancel()
		if err != nil {
			t.Fatalf("%s publish %d: %v", origin, s, err)
		}
	}
}

func TestOpsReachEveryMaster(t *testing.T) {
	buses, _ := bustest.Fleet(t)
	publish(t, buses["fi1"], "fi1", 1, 2)
	publish(t, buses["ru2"], "ru2", 1, 3)
	publish(t, buses["de2"], "de2", 1, 1)
	bustest.WaitHeads(t, buses, oplog.Vector{"fi1": 2, "ru2": 3, "de2": 1}, 60*time.Second)

	op, err := buses["de2"].Get(context.Background(), "ru2", 3)
	if err != nil || op.Origin != "ru2" || op.Seq != 3 || string(op.Fields["n"]) != "3" {
		t.Fatalf("de2 reading ru2#3: %+v %v", op, err)
	}
	if err := buses["ru2"].Publish(context.Background(), mkOp("ru2", 5)); err == nil {
		t.Fatal("seq gap accepted")
	}
	if err := buses["ru2"].Publish(context.Background(), mkOp("fi1", 3)); err == nil {
		t.Fatal("ru2 published an op of fi1")
	}
}

// Slow (~1 min): both sides of a RU|EU split keep writing and converge after.
func TestSplitKeepsBothSidesWritable(t *testing.T) {
	if os.Getenv("XUI_NATS_SPLIT_TEST") == "" {
		t.Skip("set XUI_NATS_SPLIT_TEST=1")
	}
	buses, links := bustest.Fleet(t)
	publish(t, buses["ru2"], "ru2", 1, 1)
	bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 1}, 60*time.Second)

	for k, l := range links {
		if strings.Contains(k, "ru2") {
			l.SetCut(true)
		}
	}
	publish(t, buses["ru2"], "ru2", 2, 6)
	publish(t, buses["fi1"], "fi1", 1, 3)
	publish(t, buses["de2"], "de2", 1, 2)
	if v, _ := buses["ru2"].Heads(context.Background(), bustest.Masters); v["fi1"] != 0 {
		t.Fatalf("split leaked: ru2 sees %v", v)
	}
	for _, l := range links {
		l.SetCut(false)
	}
	took := bustest.WaitHeads(t, buses, oplog.Vector{"fi1": 3, "ru2": 6, "de2": 2}, 120*time.Second)
	t.Logf("converged %s after heal", took.Round(time.Second))
}

func fleetTLS(t *testing.T, ca *fleetca.CA) func(string) *tls.Config {
	return func(name string) *tls.Config {
		cert, err := ca.Issue(name, []string{"127.0.0.1"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return ca.MutualTLS(cert)
	}
}

// Gateways speak mutual TLS with fleet certificates; a server holding a
// certificate from another CA cannot join, even knowing the address.
func TestGatewaysRequireFleetCert(t *testing.T) {
	ca, err := fleetca.New("fleet")
	if err != nil {
		t.Fatal(err)
	}
	buses, _ := bustest.FleetWith(t, fleetTLS(t, ca))
	publish(t, buses["ru2"], "ru2", 1, 2)
	bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 2}, time.Minute)
	before := buses["fi1"].InboundGateways()

	// trusts the fleet CA, so only fi1's check of the client cert can stop it
	foreign, _ := fleetca.New("not the fleet")
	own, _ := foreign.Issue("xx", []string{"127.0.0.1"}, time.Hour)
	intruderTLS := ca.MutualTLS(own)
	intruder, err := bus.Start(bus.Config{
		Name: "xx", StoreDir: t.TempDir(), Port: -1, RoutePort: bustest.FreePort(t),
		GatewayHost: "127.0.0.1", GatewayPort: bustest.FreePort(t), Advertise: "127.0.0.1:1",
		Peers: map[string]string{"fi1": buses["fi1"].GatewayURL()}, Logger: bustest.Quiet{},
		TLS: intruderTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer intruder.Close()
	time.Sleep(3 * time.Second)
	if got := buses["fi1"].InboundGateways(); !slices.Equal(got, before) || slices.Contains(got, "xx") {
		t.Fatalf("fi1 inbound gateways %v -> %v: foreign certificate accepted", before, got)
	}
	if !slices.Equal(before, []string{"de2", "ru2"}) {
		t.Fatalf("fleet members not connected over TLS: %v", before)
	}
}
