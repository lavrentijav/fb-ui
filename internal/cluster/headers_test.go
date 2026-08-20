package cluster

import (
	"strconv"
	"strings"
	"testing"
)

func TestFallbackDomains(t *testing.T) {
	tests := []struct {
		name  string
		peers []Endpoint
		want  string
	}{
		{
			name:  "renders in input order",
			peers: []Endpoint{{Domain: "sub2.example.com"}, {Domain: "sub3.example.com"}},
			want:  "sub2.example.com, sub3.example.com",
		},
		{
			name:  "deduplicates",
			peers: []Endpoint{{Domain: "sub2.example.com"}, {Domain: "sub2.example.com"}},
			want:  "sub2.example.com",
		},
		{
			name:  "trims surrounding space",
			peers: []Endpoint{{Domain: "  sub2.example.com  "}},
			want:  "sub2.example.com",
		},
		{
			name:  "drops empty entries",
			peers: []Endpoint{{Domain: ""}, {Domain: "sub2.example.com"}},
			want:  "sub2.example.com",
		},
		{
			name:  "drops header-injecting values",
			peers: []Endpoint{{Domain: "evil.com\r\nX-Injected: 1"}, {Domain: "a,b.com"}, {Domain: "ok.com"}},
			want:  "ok.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FallbackDomains(tt.peers); got != tt.want {
				t.Fatalf("FallbackDomains = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFallbackDomainsCapsCount(t *testing.T) {
	peers := make([]Endpoint, 0, MaxFallbackDomains+3)
	for i := 0; i < MaxFallbackDomains+3; i++ {
		peers = append(peers, Endpoint{Domain: string(rune('a'+i)) + ".example.com"})
	}
	got := FallbackDomains(peers)
	if n := strings.Count(got, ",") + 1; n != MaxFallbackDomains {
		t.Fatalf("emitted %d domains, want %d (%q)", n, MaxFallbackDomains, got)
	}
}

func TestFallbackIPs(t *testing.T) {
	tests := []struct {
		name  string
		peers []Endpoint
		want  string
	}{
		{
			name:  "flattens across peers",
			peers: []Endpoint{{Ips: []string{"185.1.1.1"}}, {Ips: []string{"198.51.100.3"}}},
			want:  "185.1.1.1, 198.51.100.3",
		},
		{
			name:  "keeps IPv6",
			peers: []Endpoint{{Ips: []string{"2001:db8::1"}}},
			want:  "2001:db8::1",
		},
		{
			name:  "drops non-IP values",
			peers: []Endpoint{{Ips: []string{"not-an-ip", "sub.example.com", "185.1.1.1"}}},
			want:  "185.1.1.1",
		},
		{
			name:  "deduplicates equal addresses written differently",
			peers: []Endpoint{{Ips: []string{"2001:db8::1", "2001:0db8:0000::1"}}},
			want:  "2001:db8::1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FallbackIPs(tt.peers); got != tt.want {
				t.Fatalf("FallbackIPs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFallbackIPsCapsCount(t *testing.T) {
	ips := make([]string, 0, MaxFallbackIPs+5)
	for i := 0; i < MaxFallbackIPs+5; i++ {
		ips = append(ips, "198.51.100."+strconv.Itoa(i+1))
	}
	got := FallbackIPs([]Endpoint{{Ips: ips}})
	if n := strings.Count(got, ",") + 1; n != MaxFallbackIPs {
		t.Fatalf("emitted %d IPs, want %d (%q)", n, MaxFallbackIPs, got)
	}
}
