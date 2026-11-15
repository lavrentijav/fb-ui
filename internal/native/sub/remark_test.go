package sub

import (
	"testing"
	"time"
)

func pinClock(t *testing.T, unix int64) {
	t.Helper()
	oldNow, oldUnix := timeNow, timeUnix
	timeNow = func() time.Time { return time.Unix(unix, 0) }
	timeUnix = func(sec int64) time.Time { return time.Unix(sec, 0) }
	t.Cleanup(func() { timeNow, timeUnix = oldNow, oldUnix })
}

func TestExpandRemarkRendersTheCommonTokens(t *testing.T) {
	pinClock(t, 1_700_000_000)
	v := Vars{
		Node: "ru4", Inbound: "vpn", Email: "alice", User: "alice01", Transport: "ws",
		Used: 2 << 30, Total: 10 << 30, Expire: 1_700_000_000 + 5*86400,
	}
	got := ExpandRemark("{{NODE}}-{{INBOUND}}|{{EMAIL}}|{{DAYS_LEFT}}d|{{TRAFFIC_LEFT}}", v)
	if got != "ru4-vpn|alice|5d|8GiB" {
		t.Fatalf("%q", got)
	}
}

func TestExpandRemarkDropsEmptySegmentsAndUnlimitedMarks(t *testing.T) {
	pinClock(t, 1_700_000_000)
	// no expiry and no total: both token segments resolve to nothing
	got := ExpandRemark("{{INBOUND}}-{{EMAIL}}|{{TRAFFIC_LEFT}}|{{DAYS_LEFT}}D", Vars{Inbound: "", Email: "bob"})
	if got != "bob|∞" { // the unlimited marker is a value; the days segment is dropped
		t.Fatalf("%q", got)
	}
	// unlimited traffic is a real value, so the segment stays
	if got := ExpandRemark("{{EMAIL}}|{{TRAFFIC_LEFT}}", Vars{Email: "bob"}); got != "bob|∞" {
		t.Fatalf("unlimited: %q", got)
	}
}

func TestExpandRemarkShowsStatus(t *testing.T) {
	pinClock(t, 1_700_000_000)
	if got := ExpandRemark("{{STATUS_EMOJI}}{{STATUS}}", Vars{Expire: 1_699_000_000}); got != "⛔expired" {
		t.Fatalf("expired: %q", got)
	}
	if got := ExpandRemark("{{STATUS_EMOJI}}{{STATUS}}", Vars{Total: 10, Used: 10}); got != "🚫depleted" {
		t.Fatalf("depleted: %q", got)
	}
	if got := ExpandRemark("{{STATUS}}", Vars{}); got != "active" {
		t.Fatalf("active: %q", got)
	}
}

func TestExpandRemarkLeavesLiteralTextAndUnknownTokens(t *testing.T) {
	if got := ExpandRemark("plain name", Vars{}); got != "plain name" {
		t.Fatalf("%q", got)
	}
	// an unknown token renders empty; its segment is dropped when alone
	if got := ExpandRemark("{{NOPE}}", Vars{}); got != "" {
		t.Fatalf("%q", got)
	}
	// a segment whose only token resolves to nothing is dropped whole
	if got := ExpandRemark("x-{{UNKNOWN}}y", Vars{}); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestSizeFormatting(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0B", 512: "512B", 1024: "1KiB", 1536: "1.5KiB", 1 << 30: "1GiB", 5 << 40: "5TiB",
	} {
		if got := size(n); got != want {
			t.Fatalf("size(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestExpandRemarkRendersInboundRemark(t *testing.T) {
	if got := ExpandRemark("{{REMARK}}", Vars{Remark: "frankfurt-de"}); got != "frankfurt-de" {
		t.Fatalf("%q", got)
	}
	if got := ExpandRemark("{{REMARK}}", Vars{}); got != "" {
		t.Fatalf("empty remark: %q", got)
	}
}
