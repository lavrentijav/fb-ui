// Package pick chooses the master a node reports to: the best by RTT,
// throughput, path packet size and master load, re-chosen every RebindEvery.
package pick

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"time"
)

// Measure is one probe of one master.
type Measure struct {
	RTT        time.Duration
	Throughput float64 // bytes per second
	MaxPacket  int     // TCP MSS of the path, bytes
	Load       float64 // master's load, 0 idle .. 1 saturated (can exceed 1)
}

type Prober func(ctx context.Context, master string) (Measure, error)

type Settings struct {
	RebindEvery time.Duration // 0 = 1h
	// Switch only when the challenger scores this much better (0 = 0.2), so
	// two close masters do not flap.
	Hysteresis float64
	// MinPacket: paths with a smaller MSS are last resort (fragmenting tunnels).
	MinPacket int
	// Probes of one master run with this timeout (0 = 5s).
	Timeout time.Duration
}

func (s Settings) withDefaults() Settings {
	if s.RebindEvery <= 0 {
		s.RebindEvery = time.Hour
	}
	if s.Hysteresis <= 0 {
		s.Hysteresis = 0.2
	}
	if s.Timeout <= 0 {
		s.Timeout = 5 * time.Second
	}
	return s
}

// Score is lower-better: effective latency in ms, inflated by load, slow
// throughput and a path below MinPacket.
func (s Settings) Score(m Measure) float64 {
	ms := float64(m.RTT) / float64(time.Millisecond)
	score := math.Max(ms, 1) * (1 + 2*math.Max(m.Load, 0))
	if m.Throughput > 0 {
		score += 1e6 / m.Throughput // +1 ms at 1 MB/s, +10 ms at 100 KB/s
	}
	if s.MinPacket > 0 && m.MaxPacket > 0 && m.MaxPacket < s.MinPacket {
		score *= 10
	}
	return score
}

var ErrNoMaster = errors.New("pick: no master reachable")

type Selector struct {
	masters []string
	probe   Prober
	set     Settings
	now     func() time.Time

	mu      sync.Mutex
	current string
	boundAt time.Time
	last    map[string]Measure
}

func New(masters []string, probe Prober, set Settings) *Selector {
	return &Selector{masters: slices.Clone(masters), probe: probe, set: set.withDefaults(), now: time.Now}
}

// Current is the bound master, "" before the first Tick.
func (s *Selector) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Measures are the results of the last full probe round.
func (s *Selector) Measures() map[string]Measure {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Measure, len(s.last))
	for k, v := range s.last {
		out[k] = v
	}
	return out
}

// Failed reports that sending to the current master failed: the next Tick
// re-chooses right away instead of waiting for RebindEvery.
func (s *Selector) Failed(master string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if master == s.current {
		s.boundAt = time.Time{}
	}
}

// Tick re-chooses when unbound, after RebindEvery, or after Failed, and
// returns the master to use.
func (s *Selector) Tick(ctx context.Context) (string, error) {
	s.mu.Lock()
	due := s.current == "" || s.boundAt.IsZero() || s.now().Sub(s.boundAt) >= s.set.RebindEvery
	cur := s.current
	s.mu.Unlock()
	if !due {
		return cur, nil
	}
	measures := s.probeAll(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = measures
	best, bestScore := "", math.Inf(1)
	for _, m := range s.masters {
		if res, ok := measures[m]; ok {
			if sc := s.set.Score(res); sc < bestScore {
				best, bestScore = m, sc
			}
		}
	}
	if best == "" {
		// keep the old binding: it may come back before any other does
		s.boundAt = time.Time{}
		return s.current, ErrNoMaster
	}
	if cm, ok := measures[s.current]; ok && s.current != best && !s.boundAt.IsZero() {
		if bestScore > s.set.Score(cm)*(1-s.set.Hysteresis) {
			best = s.current
		}
	}
	s.current, s.boundAt = best, s.now()
	return best, nil
}

func (s *Selector) probeAll(ctx context.Context) map[string]Measure {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]Measure{}
	for _, m := range s.masters {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, s.set.Timeout)
			defer cancel()
			if res, err := s.probe(pctx, m); err == nil {
				mu.Lock()
				out[m] = res
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return out
}
