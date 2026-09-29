package desired_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// render serves the "config" field of nodes/<node> at the master's version.
func render(m *master.Master, st *master.Mem) desired.Render {
	return func(ctx context.Context, node string) (desired.State, error) {
		rec, ok := st.State.Get("nodes", node)
		if !ok {
			return desired.State{}, errors.New("unknown node")
		}
		v, err := m.Version(ctx)
		return desired.State{Stamp: v, Config: rec["config"]}, err
	}
}

func TestNodeNeverRollsBackAcrossMasters(t *testing.T) {
	ctx := context.Background()
	buses, links := bustest.Fleet(t)
	ms := map[string]*master.Master{}
	for _, name := range bustest.Masters {
		st := master.NewMem()
		m, err := master.New(ctx, name, bustest.Masters, buses[name], st)
		if err != nil {
			t.Fatal(err)
		}
		ms[name] = m
		if _, err := desired.Serve(buses[name], name, render(m, st)); err != nil {
			t.Fatal(err)
		}
	}
	var applied []string
	dir := t.TempDir()
	app, err := desired.OpenApplier(dir, func(_ context.Context, s desired.State) error {
		applied = append(applied, string(s.Config))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conns := map[string]*nats.Conn{}
	for name, b := range buses {
		nc, err := nats.Connect(b.ClientURL())
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		conns[name] = nc
	}
	pull := func(from string) bool {
		t.Helper()
		ran, err := app.Pull(ctx, conns[from], from, "nl2")
		if err != nil {
			t.Fatalf("pull from %s: %v", from, err)
		}
		return ran
	}
	set := func(on string, cfg string) {
		t.Helper()
		if _, err := ms[on].Set(ctx, "nodes", "nl2", map[string]any{"config": json.RawMessage(cfg)}); err != nil {
			t.Fatal(err)
		}
	}

	set("ru2", `{"v":1}`)
	if !pull("ru2") || applied[0] != `{"v":1}` {
		t.Fatalf("first state not applied: %v", applied)
	}
	bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 1}, time.Minute)
	for _, m := range ms {
		if _, err := m.CatchUp(ctx); err != nil {
			t.Fatal(err)
		}
	}

	for k, l := range links {
		if strings.Contains(k, "ru2") {
			l.SetCut(true)
		}
	}
	set("ru2", `{"v":2}`)
	if !pull("ru2") {
		t.Fatal("v2 from ru2 not applied")
	}
	// failover to fi1, which has not seen v2 yet: nothing must change
	if pull("fi1") || app.Current().Config == nil || string(app.Current().Config) != `{"v":2}` {
		t.Fatalf("rolled back to fi1's older state: %s", app.Current().Config)
	}

	for _, l := range links {
		l.SetCut(false)
	}
	bustest.WaitHeads(t, buses, oplog.Vector{"ru2": 2}, 2*time.Minute)
	if _, err := ms["fi1"].CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	// an unrelated change moves the version but not nl2's config
	if _, err := ms["fi1"].Set(ctx, "nodes", "de2", map[string]any{"config": json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	before := app.Current().Stamp
	if pull("fi1") || !app.Current().Stamp.After(before) {
		t.Fatal("same config must not re-apply, but the stamp must advance")
	}
	set("fi1", `{"v":3}`)
	if !pull("fi1") || applied[len(applied)-1] != `{"v":3}` {
		t.Fatalf("v3 from fi1: %v", applied)
	}
	if len(applied) != 3 {
		t.Fatalf("applied %v, want exactly v1 v2 v3", applied)
	}
	again, err := desired.OpenApplier(dir, nil)
	if err != nil || again.Current().Hash != app.Current().Hash || again.Current().Stamp != app.Current().Stamp {
		t.Fatalf("applied state lost on restart: %+v %v", again.Current(), err)
	}
}
