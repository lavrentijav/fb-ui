package agent

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/presence"
)

type fakeOnline []string

func (f fakeOnline) Online(context.Context) ([]string, error) { return f, nil }

// A presence change re-applies the bundle; the same presence does not.
func TestPresenceChangeRepatches(t *testing.T) {
	m := &presence.Map{}
	applied := 0
	p := &Presence{Node: "ru4", Xray: fakeOnline(nil), Map: m, Every: 20 * time.Millisecond, repatch: func(context.Context) error { applied++; return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go p.watch(ctx)
	m.Add(presence.Report{Node: "de2", Users: []string{"bob"}, At: time.Now().UnixMilli()})
	time.Sleep(100 * time.Millisecond)
	m.Add(presence.Report{Node: "de2", Users: []string{"bob"}, At: time.Now().UnixMilli()})
	<-ctx.Done()
	if applied != 1 {
		t.Fatalf("repatched %d times, want 1", applied)
	}
}
