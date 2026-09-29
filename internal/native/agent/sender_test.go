package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

// nl2 reports to the nearest master (ru2); ru2 dies, the next flush fails,
// and the one after lands the rest on fi1 without losing or renumbering.
func TestSenderFailsOverToNextNearest(t *testing.T) {
	buses, _ := bustest.Fleet(t)
	var mu sync.Mutex
	rtt := map[string]time.Duration{"ru2": 5 * time.Millisecond, "fi1": 30 * time.Millisecond, "de2": 40 * time.Millisecond}
	prober := func(_ context.Context, m string) (pick.Measure, error) {
		mu.Lock()
		defer mu.Unlock()
		r, ok := rtt[m]
		if !ok {
			return pick.Measure{}, errors.New("down")
		}
		return pick.Measure{RTT: r}, nil
	}
	j, err := agent.OpenJournal(t.TempDir(), "nl2")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s := &agent.Sender{
		Journal: j, Select: pick.New(bustest.Masters, prober, pick.Settings{}), Timeout: 2 * time.Second,
		Dial: func(ctx context.Context, m string) (*nats.Conn, error) {
			return nats.Connect(buses[m].ClientURL(), nats.NoReconnect(), nats.Timeout(2*time.Second))
		},
	}
	defer s.Close()
	ctx := context.Background()
	for range 3 {
		if _, err := j.Add(reports.Report{Online: []string{"alice"}}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Flush(ctx); n != 3 || err != nil {
		t.Fatalf("first flush %d %v", n, err)
	}
	if _, last, _ := buses["ru2"].ReportRange(ctx, "ru2"); last != 3 {
		t.Fatalf("ru2 holds %d reports", last)
	}

	buses["ru2"].Close()
	mu.Lock()
	delete(rtt, "ru2")
	mu.Unlock()
	for range 2 {
		if _, err := j.Add(reports.Report{Online: []string{"alice"}}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Flush(ctx); n != 0 || err == nil {
		t.Fatalf("flush to dead ru2: %d %v", n, err)
	}
	if n, err := s.Flush(ctx); n != 2 || err != nil {
		t.Fatalf("failover flush %d %v", n, err)
	}
	if len(j.Pending(10)) != 0 {
		t.Fatal("journal not drained")
	}
	for i, want := range []uint64{4, 5} {
		raw, err := buses["fi1"].GetReport(ctx, "fi1", uint64(i+1))
		var r reports.Report
		if err != nil || json.Unmarshal(raw, &r) != nil || r.Seq != want || r.Node != "nl2" {
			t.Fatalf("fi1 report %d: %s %v", i+1, raw, err)
		}
	}
}
