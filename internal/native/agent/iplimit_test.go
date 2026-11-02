package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/failban"
	"github.com/mhsanaei/3x-ui/v3/internal/native/xrayrun"
)

type fakeIPs map[string][]xrayrun.OnlineIP

func (f fakeIPs) OnlineIPs(context.Context) (map[string][]xrayrun.OnlineIP, error) {
	return f, nil
}

type fakeBans struct {
	limits map[string]int
	calls  [][]string
}

func (f *fakeBans) Limits() map[string]int { return f.limits }
func (f *fakeBans) SetBans(_ context.Context, bans []string) error {
	f.calls = append(f.calls, bans)
	return nil
}

// The oldest address of a user over their limit is banned on the node.
func TestIPLimitBansTheOlderAddress(t *testing.T) {
	applier := &fakeBans{limits: map[string]int{"alice": 1}}
	p := &IPLimit{
		Xray: fakeIPs{"alice": {
			{Email: "alice", IP: "10.0.0.1", LastSeen: 100},
			{Email: "alice", IP: "10.0.0.2", LastSeen: 200},
		}},
		Applier: applier,
		Limiter: &failban.Limiter{},
	}
	p.tick(context.Background())
	p.tick(context.Background())
	if len(applier.calls) != 2 || !slices.Equal(applier.calls[0], []string{"10.0.0.1"}) ||
		!slices.Equal(applier.calls[1], []string{"10.0.0.1"}) {
		t.Fatalf("calls %v", applier.calls)
	}
}
