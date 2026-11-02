package nodestate

import (
	"context"
	"encoding/json"
	"time"
)

// Source is the report streams of every master (the bus implements it).
type Source interface {
	ReportRange(ctx context.Context, master string) (first, last uint64, err error)
	GetReport(ctx context.Context, master string, seq uint64) ([]byte, error)
}

// Collect reads the newest report per node across every master. perStream
// bounds the scan so a master returning after a long pause is not slow; the
// newest reports are at the end of each stream, which is where it starts.
func Collect(ctx context.Context, src Source, masters []string, perStream int) []Report {
	if perStream <= 0 {
		perStream = 2000
	}
	seen := map[string]bool{}
	var out []Report
	for _, master := range masters {
		first, last, err := src.ReportRange(ctx, master)
		if err != nil || last == 0 {
			continue
		}
		from := uint64(1)
		if last > uint64(perStream) {
			from = last - uint64(perStream)
		}
		if first > from {
			from = first
		}
		for seq := last; seq >= from && seq > 0; seq-- {
			raw, err := src.GetReport(ctx, master, seq)
			if err != nil {
				break
			}
			r, ok := decode(raw)
			if ok && !seen[r.Node] {
				seen[r.Node] = true
				out = append(out, r)
			}
			if seq == from {
				break
			}
		}
	}
	return out
}

func decode(raw []byte) (Report, bool) {
	var wire struct {
		Node   string   `json:"n"`
		At     int64    `json:"at"`
		Online []string `json:"o"`
		Drain  *struct {
			Since  int64 `json:"since"`
			Active int   `json:"active"`
		} `json:"dr"`
		Health *struct {
			Load1    float64 `json:"l1"`
			MemUsed  int64   `json:"mu"`
			MemTotal int64   `json:"mt"`
			DiskFree int64   `json:"df"`
			Uptime   int64   `json:"up"`
			Version  string  `json:"v"`
		} `json:"h"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Node == "" {
		return Report{}, false
	}
	r := Report{Node: wire.Node, At: wire.At, Online: wire.Online}
	if wire.Drain != nil {
		r.Drain = &Drain{Since: wire.Drain.Since, Active: wire.Drain.Active}
	}
	if wire.Health != nil {
		r.Health = &Health{
			Load1: wire.Health.Load1, MemUsed: wire.Health.MemUsed, MemTotal: wire.Health.MemTotal,
			DiskFree: wire.Health.DiskFree, Uptime: wire.Health.Uptime, Version: wire.Health.Version,
		}
	}
	return r, true
}

// Age is how long ago the report was sent.
func (r Report) Age(now time.Time) time.Duration {
	if r.At == 0 {
		return 1<<63 - 1
	}
	return now.Sub(time.UnixMilli(r.At))
}
