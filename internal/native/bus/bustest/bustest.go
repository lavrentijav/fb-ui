// Package bustest runs a three-master bus fleet in-process with cuttable links.
package bustest

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Quiet drops server logs.
type Quiet struct{}

func (Quiet) Noticef(string, ...any) {}
func (Quiet) Warnf(string, ...any)   {}
func (Quiet) Fatalf(string, ...any)  {}
func (Quiet) Errorf(string, ...any)  {}
func (Quiet) Debugf(string, ...any)  {}
func (Quiet) Tracef(string, ...any)  {}

// link is a TCP proxy between two gateways; cutting it splits the network.
type Link struct {
	ln    net.Listener
	mu    sync.Mutex
	cut   bool
	conns []net.Conn
}

func newLink(t *testing.T, to string) *Link {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &Link{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.mu.Lock()
			d, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", to)
			if l.cut || err != nil {
				l.mu.Unlock()
				c.Close()
				if d != nil {
					d.Close()
				}
				continue
			}
			l.conns = append(l.conns, c, d)
			l.mu.Unlock()
			go func() { _, _ = io.Copy(d, c); d.Close() }()
			go func() { _, _ = io.Copy(c, d); c.Close() }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return l
}

// SetCut splits (true) or heals (false) this direction.
func (l *Link) SetCut(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cut = v
	for _, c := range l.conns {
		c.Close()
	}
	l.conns = nil
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// Masters are the fleet's names.
var Masters = []string{"fi1", "ru2", "de2"}

// Fleet starts three masters whose gateways reach each other only through
// links["a>b"], and waits until JetStream accepts the streams.
func Fleet(t *testing.T) (map[string]*bus.Bus, map[string]*Link) {
	t.Helper()
	gw := map[string]int{}
	for _, m := range Masters {
		gw[m] = freePort(t)
	}
	links := map[string]*Link{}
	for _, a := range Masters {
		for _, b := range Masters {
			if a != b {
				links[a+">"+b] = newLink(t, fmt.Sprintf("127.0.0.1:%d", gw[b]))
			}
		}
	}
	buses := map[string]*bus.Bus{}
	for _, m := range Masters {
		peers := map[string]string{}
		for _, p := range Masters {
			if p != m {
				peers[p] = "nats://" + links[m+">"+p].ln.Addr().String()
			}
		}
		b, err := bus.Start(bus.Config{
			Name: m, StoreDir: t.TempDir(), Port: -1, RoutePort: freePort(t),
			GatewayHost: "127.0.0.1", GatewayPort: gw[m], Advertise: "127.0.0.1:1",
			Peers: peers, Logger: Quiet{},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.Close)
		buses[m] = b
	}
	deadline := time.Now().Add(30 * time.Second)
	for _, m := range Masters {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := buses[m].Ensure(ctx, Masters)
			cancel()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("ensure %s: %v", m, err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	return buses, links
}

// WaitHeads waits until every master holds want, and returns how long it took.
func WaitHeads(t *testing.T, buses map[string]*bus.Bus, want oplog.Vector, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		ok := true
		for _, m := range Masters {
			v, err := buses[m].Heads(context.Background(), Masters)
			if err != nil || len(v) != len(want) {
				ok = false
				continue
			}
			for k, n := range want {
				if v[k] != n {
					ok = false
				}
			}
		}
		if ok {
			return time.Since(start)
		}
		if time.Since(start) > within {
			for _, m := range Masters {
				v, _ := buses[m].Heads(context.Background(), Masters)
				t.Logf("%s heads %v", m, v)
			}
			t.Fatalf("heads did not reach %v in %s", want, within)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
