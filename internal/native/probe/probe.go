// Package probe measures masters for pick over a real NATS connection:
// RTT, throughput of a sized reply, TCP MSS of the path and the master's load.
package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
)

// Config says how to reach each master.
type Config struct {
	URLs  map[string]string // master name -> nats URL of its client listener
	TLS   *tls.Config
	Bytes int // throughput sample size (0 = 256 KiB)
	Pings int // RTT samples, the minimum counts (0 = 3)
	// Inbox is the node's reply prefix (bus.NodeInbox) when masters check certificates.
	Inbox string
}

// NATS returns a pick.Prober that opens a fresh connection per probe, so
// the MSS and RTT describe the path as it is now.
func NATS(cfg Config) pick.Prober {
	if cfg.Bytes <= 0 {
		cfg.Bytes = 256 << 10
	}
	cfg.Bytes = min(cfg.Bytes, bus.MaxProbeBytes)
	if cfg.Pings <= 0 {
		cfg.Pings = 3
	}
	return func(ctx context.Context, master string) (pick.Measure, error) {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
		}
		url, ok := cfg.URLs[master]
		if !ok {
			return pick.Measure{}, fmt.Errorf("probe: no URL for %s", master)
		}
		d := &tapDialer{}
		if deadline, ok := ctx.Deadline(); ok {
			d.Deadline = deadline
		}
		opts := []nats.Option{nats.SetCustomDialer(d), nats.NoReconnect(), nats.Name("probe")}
		if cfg.TLS != nil {
			opts = append(opts, nats.Secure(cfg.TLS))
		}
		if cfg.Inbox != "" {
			opts = append(opts, nats.CustomInboxPrefix(cfg.Inbox))
		}
		nc, err := nats.Connect(url, opts...)
		if err != nil {
			return pick.Measure{}, err
		}
		defer nc.Close()

		var m pick.Measure
		for range cfg.Pings {
			r, err := nc.RTT()
			if err != nil {
				return pick.Measure{}, err
			}
			if m.RTT == 0 || r < m.RTT {
				m.RTT = r
			}
		}
		start := time.Now()
		reply, err := nc.RequestWithContext(ctx, bus.ProbeSubject(master), []byte(strconv.Itoa(cfg.Bytes)))
		if err != nil {
			return pick.Measure{}, err
		}
		if took := time.Since(start) - m.RTT; took > 0 {
			m.Throughput = float64(len(reply.Data)) / took.Seconds()
		}
		m.Load, _ = strconv.ParseFloat(reply.Header.Get("Load"), 64)
		m.MaxPacket = d.mss()
		return m, nil
	}
}

// tapDialer keeps the TCP connection so its MSS can be read after connect.
type tapDialer struct {
	net.Dialer
	mu   sync.Mutex
	conn net.Conn
}

func (d *tapDialer) Dial(network, address string) (net.Conn, error) {
	c, err := d.Dialer.Dial(network, address)
	if err == nil {
		d.mu.Lock()
		d.conn = c
		d.mu.Unlock()
	}
	return c, err
}

func (d *tapDialer) mss() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	tc, ok := d.conn.(*net.TCPConn)
	if !ok {
		return 0
	}
	return tcpMSS(tc)
}

// SystemLoad is the 1-minute load average per CPU; a master's default load.
func SystemLoad() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return v / float64(runtime.NumCPU())
}
