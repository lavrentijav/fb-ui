// Package hlc stamps events so masters order them the same way everywhere:
// wall ms + logical counter + origin id, monotonic across clock steps.
package hlc

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stamp orders one event. The zero Stamp is older than every real one.
type Stamp struct {
	Wall    int64  `json:"w"` // unix milliseconds
	Logical uint32 `json:"l"`
	Origin  string `json:"o"`
}

// Compare returns -1, 0 or 1.
func (s Stamp) Compare(o Stamp) int {
	switch {
	case s.Wall != o.Wall:
		return cmp(s.Wall < o.Wall)
	case s.Logical != o.Logical:
		return cmp(s.Logical < o.Logical)
	default:
		return strings.Compare(s.Origin, o.Origin)
	}
}

func cmp(less bool) int {
	if less {
		return -1
	}
	return 1
}

func (s Stamp) After(o Stamp) bool { return s.Compare(o) > 0 }
func (s Stamp) IsZero() bool       { return s == Stamp{} }

// String is "wall.logical@origin", which Parse reads back.
func (s Stamp) String() string {
	return strconv.FormatInt(s.Wall, 10) + "." + strconv.FormatUint(uint64(s.Logical), 10) + "@" + s.Origin
}

func Parse(v string) (Stamp, error) {
	head, origin, ok := strings.Cut(v, "@")
	wall, logical, ok2 := strings.Cut(head, ".")
	if !ok || !ok2 {
		return Stamp{}, fmt.Errorf("hlc: bad stamp %q", v)
	}
	w, err := strconv.ParseInt(wall, 10, 64)
	if err != nil {
		return Stamp{}, fmt.Errorf("hlc: bad stamp %q: %w", v, err)
	}
	l, err := strconv.ParseUint(logical, 10, 32)
	if err != nil {
		return Stamp{}, fmt.Errorf("hlc: bad stamp %q: %w", v, err)
	}
	return Stamp{Wall: w, Logical: uint32(l), Origin: origin}, nil
}

// Clock issues stamps for one origin.
type Clock struct {
	origin string
	now    func() int64
	mu     sync.Mutex
	last   Stamp
}

func New(origin string) *Clock {
	return &Clock{origin: origin, now: func() int64 { return time.Now().UnixMilli() }}
}

// NewWithTime lets tests drive the wall clock.
func NewWithTime(origin string, now func() int64) *Clock {
	return &Clock{origin: origin, now: now}
}

// Now stamps a local event.
func (c *Clock) Now() Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.now(); w > c.last.Wall {
		c.last = Stamp{Wall: w}
	} else {
		c.last.Logical++
	}
	c.last.Origin = c.origin
	return c.last
}

// Observe moves the clock past a stamp received from another origin, so
// the next local event is ordered after it.
func (c *Clock) Observe(remote Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remote.Wall > c.last.Wall || (remote.Wall == c.last.Wall && remote.Logical > c.last.Logical) {
		c.last = Stamp{Wall: remote.Wall, Logical: remote.Logical, Origin: c.origin}
	}
}

// Restore continues after the newest stamp this origin issued before a
// restart, even if the host clock is now behind it.
func (c *Clock) Restore(last Stamp) { c.Observe(last) }
