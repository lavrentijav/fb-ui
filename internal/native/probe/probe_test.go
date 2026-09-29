package probe_test

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus/bustest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/probe"
)

// slowProxy forwards to addr, holding every chunk for delay each way.
func slowProxy(t *testing.T, addr string, delay time.Duration) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				time.Sleep(delay)
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		dst.Close()
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
			if err != nil {
				c.Close()
				continue
			}
			go pipe(d, c)
			go pipe(c, d)
		}
	}()
	return "nats://" + ln.Addr().String()
}

func hostPort(url string) string { return strings.TrimPrefix(url, "nats://") }

// A node picks fi1: ru2 is 40 ms away, de2 is overloaded.
func TestNodePicksByRealMeasures(t *testing.T) {
	buses, _ := bustest.Fleet(t)
	loads := map[string]float64{"fi1": 0.1, "ru2": 0.1, "de2": 4}
	for name, b := range buses {
		load := loads[name]
		if err := b.ServeProbe(func() float64 { return load }); err != nil {
			t.Fatal(err)
		}
	}
	urls := map[string]string{
		"fi1": buses["fi1"].ClientURL(),
		"ru2": slowProxy(t, hostPort(buses["ru2"].ClientURL()), 20*time.Millisecond),
		"de2": buses["de2"].ClientURL(),
	}
	prober := probe.NATS(probe.Config{URLs: urls, Bytes: 64 << 10})

	m, err := prober(context.Background(), "fi1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Load != 0.1 || m.RTT <= 0 || m.Throughput <= 0 {
		t.Fatalf("fi1 measure %+v", m)
	}
	if runtime.GOOS == "linux" && m.MaxPacket <= 0 {
		t.Fatalf("no MSS read on linux: %+v", m)
	}
	slow, err := prober(context.Background(), "ru2")
	if err != nil || slow.RTT < 40*time.Millisecond {
		t.Fatalf("ru2 through a 2x20 ms proxy: %+v %v", slow, err)
	}

	sel := pick.New(bustest.Masters, prober, pick.Settings{})
	got, err := sel.Tick(context.Background())
	if err != nil || got != "fi1" {
		t.Fatalf("picked %s (%v), measures %+v", got, err, sel.Measures())
	}
}

func TestProbeCapsRequestedBytes(t *testing.T) {
	buses, _ := bustest.Fleet(t)
	b := buses["fi1"]
	if err := b.ServeProbe(func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	prober := probe.NATS(probe.Config{URLs: map[string]string{"fi1": b.ClientURL()}, Bytes: 64 << 20})
	start := time.Now()
	if _, err := prober(context.Background(), "fi1"); err != nil {
		t.Fatalf("oversized sample must be capped, not fail: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("probe hung")
	}
}
