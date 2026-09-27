package edge

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Drain counts the tunnels the edge carries and, while on, refuses new ones and
// closes idle ones; panel traffic is neither counted nor refused.
type Drain struct {
	mu    sync.Mutex
	conns map[*trackedConn]struct{}

	on    atomic.Bool
	idle  atomic.Int64 // nanoseconds
	since atomic.Int64 // unix seconds the drain began
	stop  chan struct{}
}

func NewDrain() *Drain {
	return &Drain{conns: map[*trackedConn]struct{}{}}
}

// DefaultDrainIdle closes a tunnel that carried nothing for this long.
const DefaultDrainIdle = 2 * time.Minute

// Set turns draining on or off. Turning it on again only changes the idle
// timeout, so the start time survives a repeated request.
func (d *Drain) Set(on bool, idle time.Duration) {
	if idle <= 0 {
		idle = DefaultDrainIdle
	}
	d.idle.Store(int64(idle))
	d.mu.Lock()
	defer d.mu.Unlock()
	if on == d.on.Load() {
		return
	}
	d.on.Store(on)
	if !on {
		d.since.Store(0)
		close(d.stop)
		d.stop = nil
		return
	}
	d.since.Store(time.Now().Unix())
	d.stop = make(chan struct{})
	go d.reap(d.stop)
}

// Draining reports whether new tunnels are refused.
func (d *Drain) Draining() bool { return d != nil && d.on.Load() }

// DrainState is what a master shows for the node.
type DrainState struct {
	Enable      bool  `json:"enable" example:"true"`
	IdleSeconds int   `json:"idleSeconds" example:"120"`
	Since       int64 `json:"since" example:"1700000000"`
	Active      int   `json:"active" example:"12"`
}

func (d *Drain) State() DrainState {
	if d == nil {
		return DrainState{}
	}
	d.mu.Lock()
	active := len(d.conns)
	d.mu.Unlock()
	return DrainState{
		Enable:      d.on.Load(),
		IdleSeconds: int(time.Duration(d.idle.Load()) / time.Second),
		Since:       d.since.Load(),
		Active:      active,
	}
}

// Track counts c until it is closed and records when data last moved. The
// peers are closed with it, so a reaped tunnel releases both of its ends.
func (d *Drain) Track(c net.Conn, peers ...io.Closer) net.Conn {
	if d == nil {
		return c
	}
	t := &trackedConn{Conn: c, d: d, peers: peers}
	t.touch()
	d.mu.Lock()
	d.conns[t] = struct{}{}
	d.mu.Unlock()
	return t
}

func (d *Drain) reap(stop chan struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			cutoff := now.Add(-time.Duration(d.idle.Load())).UnixNano()
			var stale []*trackedConn
			d.mu.Lock()
			for t := range d.conns {
				if t.last.Load() < cutoff {
					stale = append(stale, t)
				}
			}
			d.mu.Unlock()
			for _, t := range stale {
				_ = t.Close()
			}
		}
	}
}

type trackedConn struct {
	net.Conn
	d     *Drain
	peers []io.Closer
	last  atomic.Int64
	once  sync.Once
}

func (t *trackedConn) touch() { t.last.Store(time.Now().UnixNano()) }

func (t *trackedConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.touch()
	}
	return n, err
}

func (t *trackedConn) Write(p []byte) (int, error) {
	n, err := t.Conn.Write(p)
	if n > 0 {
		t.touch()
	}
	return n, err
}

func (t *trackedConn) Close() error {
	t.once.Do(func() {
		t.d.mu.Lock()
		delete(t.d.conns, t)
		t.d.mu.Unlock()
		for _, p := range t.peers {
			_ = p.Close()
		}
	})
	return t.Conn.Close()
}

// CloseWrite keeps half-closes working through the wrapper.
func (t *trackedConn) CloseWrite() error {
	if cw, ok := t.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return t.Close()
}
