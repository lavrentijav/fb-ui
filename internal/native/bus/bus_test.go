package bus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
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
