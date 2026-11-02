package reports

import "testing"

func TestMeminfoReadsAvailable(t *testing.T) {
	raw := "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    4000000 kB\n"
	total, used := meminfo(raw)
	if total != 16_000_000*1024 {
		t.Fatalf("total %d", total)
	}
	if used != 12_000_000*1024 {
		t.Fatalf("used %d", used)
	}
	// a kernel without MemAvailable leaves used at zero rather than wrong
	if total, used := meminfo("MemTotal: 1000 kB\n"); total != 1024*1000 || used != 0 {
		t.Fatalf("no available: %d %d", total, used)
	}
}

func TestHealthOfToleratesMissingFiles(t *testing.T) {
	// on a platform without /proc every field is zero, never an error
	h := HealthOf()
	if h.MemTotal < 0 || h.Uptime < 0 || h.Load1 < 0 {
		t.Fatalf("negative health %+v", h)
	}
}
