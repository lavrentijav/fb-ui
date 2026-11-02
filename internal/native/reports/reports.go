// Package reports is what a node reports (traffic deltas, onlines) and how
// it sends one; reportdb projects them on masters.
package reports

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

// Report is one node's delta since its previous report; Seq is the node's
// own counter, so a report re-sent to another master is recognized.
type Report struct {
	Node    string    `json:"n"`
	Seq     uint64    `json:"s"`
	At      int64     `json:"at"` // unix ms
	Traffic []Traffic `json:"t,omitempty"`
	Online  []string  `json:"o,omitempty"`
	// Drain is set while the node is in maintenance.
	Drain *Drain `json:"dr,omitempty"`
	// Health is the node's vitals, sent at most every report; a missing one
	// keeps the last known values on the master.
	Health *Health `json:"h,omitempty"`
}

// Drain is how many tunnels a draining node still carries.
type Drain struct {
	Since  int64 `json:"since"` // unix seconds
	Active int   `json:"active"`
}

type Traffic struct {
	Email string `json:"e"`
	Up    int64  `json:"u"`
	Down  int64  `json:"d"`
}

// Send publishes r to the master the node is bound to and waits for the
// stream's ack; an error means "send again", possibly to another master.
func Send(ctx context.Context, nc *nats.Conn, master string, r Report) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, wire.ReportSubject(master, r.Node), data,
		jetstream.WithMsgID(r.Node+":"+strconv.FormatUint(r.Seq, 10)))
	return err
}

// HealthOf reads the node's vitals; an unreadable file leaves a field zero.
func HealthOf() Health {
	var h Health
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			h.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		h.MemTotal, h.MemUsed = meminfo(string(b))
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				h.Uptime = int64(v)
			}
		}
	}
	return h
}

// meminfo reads MemTotal and MemAvailable (kB) and answers bytes used.
func meminfo(raw string) (total, used int64) {
	var available int64
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	if total > 0 && available > 0 && available < total {
		used = total - available
	}
	return total, used
}
