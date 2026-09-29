package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/desired"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

type fakeXray struct {
	mu      sync.Mutex
	applied [][]byte
	running bool
	traffic []xrayrun.UserTraffic
}

func (f *fakeXray) Apply(_ context.Context, c []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied, f.running = append(f.applied, c), true
	return nil
}

func (f *fakeXray) Restart() error { f.mu.Lock(); defer f.mu.Unlock(); f.running = true; return nil }
func (f *fakeXray) Running() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.running }
func (f *fakeXray) Traffic(context.Context) ([]xrayrun.UserTraffic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.traffic
	f.traffic = nil
	return t, nil
}

// A config written on the master reaches Xray on the node, and the traffic
// Xray counts comes back to the master as a report.
func TestNodeRoundTrip(t *testing.T) {
	ctx := context.Background()
	buses, _ := bustest.Fleet(t)
	store := master.NewMem()
	ru2, err := master.New(ctx, "ru2", bustest.Masters, buses["ru2"], store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desired.Serve(buses["ru2"].Conn(), "ru2", func(ctx context.Context, node string) (desired.State, error) {
		rec, _ := store.State.Get("nodes", node)
		v, err := ru2.Version(ctx)
		return desired.State{Stamp: v, Config: rec["config"]}, err
	}); err != nil {
		t.Fatal(err)
	}

	xr := &fakeXray{}
	dir := t.TempDir()
	app, err := desired.OpenApplier(dir+"/state", func(ctx context.Context, st desired.State) error { return xr.Apply(ctx, st.Config) })
	if err != nil {
		t.Fatal(err)
	}
	j, err := agent.OpenJournal(dir+"/journal", "nl2")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	near := func(_ context.Context, m string) (pick.Measure, error) {
		return pick.Measure{RTT: map[string]time.Duration{"ru2": time.Millisecond, "fi1": 50 * time.Millisecond, "de2": 60 * time.Millisecond}[m]}, nil
	}
	n := &agent.Node{
		Name: "nl2", Applier: app, Xray: xr,
		Sender: &agent.Sender{
			Journal: j, Select: pick.New(bustest.Masters, near, pick.Settings{}),
			Dial: func(_ context.Context, m string) (*nats.Conn, error) { return nats.Connect(buses[m].ClientURL()) },
		},
	}
	defer n.Sender.Close()

	if _, err := ru2.Set(ctx, "nodes", "nl2", map[string]any{"config": json.RawMessage(`{"inbounds":[]}`)}); err != nil {
		t.Fatal(err)
	}
	n.Pull(ctx)
	if len(xr.applied) != 1 || string(xr.applied[0]) != `{"inbounds":[]}` {
		t.Fatalf("xray got %q", xr.applied)
	}

	xr.traffic = []xrayrun.UserTraffic{{Email: "alice", Up: 5, Down: 50}}
	n.Collect(ctx)
	if sent, err := n.Sender.Flush(ctx); sent != 1 || err != nil {
		t.Fatalf("flush %d %v", sent, err)
	}
	raw, err := buses["ru2"].GetReport(ctx, "ru2", 1)
	var r reports.Report
	if err != nil || json.Unmarshal(raw, &r) != nil || r.Node != "nl2" || r.Traffic[0].Down != 50 {
		t.Fatalf("report on ru2: %s %v", raw, err)
	}
	n.Collect(ctx) // nothing counted: no empty report
	if len(j.Pending(10)) != 0 {
		t.Fatal("empty traffic produced a report")
	}
}
