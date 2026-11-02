package failban_test

import (
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/failban"
)

func limiter(now *time.Time, hold time.Duration) *failban.Limiter {
	return &failban.Limiter{Hold: hold, Now: func() time.Time { return *now }}
}

func TestKeepsTheNewestAddresses(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, time.Hour)
	obs := map[string]map[string]int64{
		"alice": {"10.0.0.1": 100, "10.0.0.2": 200, "10.0.0.3": 300},
	}
	if got := l.Observe(obs, map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("limit 1 bans %v", got)
	}
	// a higher limit releases the older address
	if got := l.Observe(obs, map[string]int{"alice": 2}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("limit 2 bans %v", got)
	}
	// no limit means no ban
	if got := l.Observe(obs, nil); len(got) != 0 {
		t.Fatalf("a lifted limit still bans: %v", got)
	}
}

func TestEachUserLimitIsItsOwn(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, time.Hour)
	obs := map[string]map[string]int64{
		"alice": {"10.0.0.1": 100, "10.0.0.2": 200},
		"bob":   {"10.0.0.3": 100, "10.0.0.4": 200},
	}
	if got := l.Observe(obs, map[string]int{"alice": 1, "bob": 5}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("bans %v", got)
	}
}

func TestReleasedAfterTheHold(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, 10*time.Minute)
	obs := map[string]map[string]int64{"alice": {"10.0.0.1": 100, "10.0.0.2": 200}}
	l.Observe(obs, map[string]int{"alice": 1})
	// the banned device leaves: only the newest address is seen from now on
	now = now.Add(5 * time.Minute)
	only := map[string]map[string]int64{"alice": {"10.0.0.2": now.Unix()}}
	if got := l.Observe(only, map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("released too early: %v", got)
	}
	now = now.Add(11 * time.Minute)
	if got := l.Observe(only, map[string]int{"alice": 1}); len(got) != 0 {
		t.Fatalf("still banned after the hold: %v", got)
	}
}

func TestAStillOverLimitAddressStaysBanned(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, 10*time.Minute)
	obs := map[string]map[string]int64{"alice": {"10.0.0.1": 100, "10.0.0.2": 200}}
	for range 3 {
		now = now.Add(9 * time.Minute)
		if got := l.Observe(obs, map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.1"}) {
			t.Fatalf("banned set %v", got)
		}
	}
}

func TestABannedDeviceReconnectingStaysBanned(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, 2*time.Minute)
	obs := map[string]map[string]int64{"alice": {"10.0.0.1": 100, "10.0.0.2": 200}}
	l.Observe(obs, map[string]int{"alice": 1})
	// it drops and reconnects a second later: still the older one of the two
	now = now.Add(time.Second)
	if got := l.Observe(obs, map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("bans %v", got)
	}
}

func TestTiesAreStable(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, time.Hour)
	obs := map[string]map[string]int64{"alice": {"10.0.0.9": 100, "10.0.0.2": 100}}
	if got := l.Observe(obs, map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.2"}) {
		t.Fatalf("tie goes to the higher address: %v", got)
	}
}

// The address that appeared first is the older one, even when the core
// reports a newer lastSeen for it.
func TestFirstSeenDecides(t *testing.T) {
	now := time.Unix(1000, 0)
	l := limiter(&now, time.Hour)
	l.Observe(map[string]map[string]int64{"alice": {"10.0.0.1": 100}}, map[string]int{"alice": 1})
	now = now.Add(time.Second)
	if got := l.Observe(map[string]map[string]int64{"alice": {"10.0.0.1": 900, "10.0.0.2": 200}},
		map[string]int{"alice": 1}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("bans %v", got)
	}
}
