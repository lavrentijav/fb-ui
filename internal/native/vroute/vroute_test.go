package vroute_test

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/vroute"
)

const uid = "11111111-1111-4111-8111-111111111111"

type rule struct {
	InboundTag  []string `json:"inboundTag"`
	User        []string `json:"user"`
	IP          []string `json:"ip"`
	OutboundTag string   `json:"outboundTag"`
}

func base(t *testing.T) []byte {
	t.Helper()
	b, err := nodespec.Render(nodespec.Spec{Node: "ru4", Inbounds: []nodespec.Inbound{
		{Tag: "vpn", Port: 1, Network: "ws", Path: "/v", Route: nodespec.Direct},
		{Tag: "cascade", Port: 2, Network: "ws", Path: "/c", Route: nodespec.Direct},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b.Xray
}

func table() *nodespec.VNet {
	return &nodespec.VNet{
		Members: []nodespec.VMember{
			{Email: "alice", IP: "100.64.0.1", Network: "n1"},
			{Email: "bob", IP: "100.64.0.2", Network: "n1"},
			{Email: "eve", IP: "100.64.0.3", Network: "n2"},
		},
		Peers: map[string]nodespec.Hop{"de2": {Address: "de2.x", Port: 443, ID: uid, Network: "ws", Path: "/c", SNI: "de2.x"}},
		Entry: []string{"vpn"}, Cascade: "cascade",
		Zone:      nodespec.VNetZoneDefault,
		Resolvers: []nodespec.VResolver{{Network: "n1", Port: 15353}, {Network: "n2", Port: 15354}},
	}
}

func parse(t *testing.T, x []byte) ([]rule, []string) {
	t.Helper()
	var cfg struct {
		Routing   struct{ Rules []rule } `json:"routing"`
		Outbounds []struct{ Tag string } `json:"outbounds"`
	}
	if err := json.Unmarshal(x, &cfg); err != nil {
		t.Fatal(err)
	}
	var outs []string
	for _, o := range cfg.Outbounds {
		outs = append(outs, o.Tag)
	}
	return cfg.Routing.Rules, outs
}

// to is where Xray sends a connection to ip arriving on inbound: the first
// rule whose addresses (if any) hold ip and whose inbounds (if any) include it.
func to(rules []rule, ip, inbound string) string {
	addr := netip.MustParseAddr(ip)
	for _, r := range rules {
		held := len(r.IP) == 0 || slices.ContainsFunc(r.IP, func(p string) bool {
			if pre, err := netip.ParsePrefix(p); err == nil {
				return pre.Contains(addr)
			}
			return p == ip
		})
		if held && (len(r.InboundTag) == 0 || slices.Contains(r.InboundTag, inbound)) {
			return r.OutboundTag
		}
	}
	return ""
}

func TestPatchRoutesMembers(t *testing.T) {
	x, err := vroute.Patch(base(t), table(), map[string]string{"alice": "ru4", "bob": "de2", "eve": "ru4"}, "ru4")
	if err != nil {
		t.Fatal(err)
	}
	rules, outs := parse(t, x)
	if got := to(rules, "100.64.0.1", "vpn"); got != "u-alice" {
		t.Fatalf("alice (here) routed to %q", got)
	}
	if got := to(rules, "100.64.0.1", "cascade"); got != "u-alice" {
		t.Fatalf("cascade delivery to alice: %q", got)
	}
	if got := to(rules, "100.64.0.2", "vpn"); got != "vnet-de2" || !slices.Contains(outs, "vnet-de2") {
		t.Fatalf("bob (on de2) routed to %q, outbounds %v", got, outs)
	}
	for _, r := range rules {
		if slices.Contains(r.IP, "100.64.0.1") && slices.Contains(r.User, "eve") {
			t.Fatal("eve (another network) may reach alice")
		}
	}
	// the range is blocked before the inbounds' own routes could send it out
	if got := to(rules, "100.64.9.9", "vpn"); got != nodespec.Block {
		t.Fatalf("an address outside the table goes to %q", got)
	}
	if rules[0].OutboundTag != "api" {
		t.Fatal("the api rule is no longer first")
	}
}

// Review focus 1 and 3: without presence, or with the user newest on
// another node, nothing routes into this node's (possibly stale) tunnel.
func TestPatchIgnoresStaleTunnels(t *testing.T) {
	x, _ := vroute.Patch(base(t), table(), map[string]string{"bob": "de2"}, "ru4")
	rules, _ := parse(t, x)
	if got := to(rules, "100.64.0.1", "vpn"); got != nodespec.Block {
		t.Fatalf("alice offline, still routed to %q", got)
	}
	if got := to(rules, "100.64.0.2", "cascade"); got != nodespec.Block {
		t.Fatalf("cascade traffic for bob (on de2) delivered here: %q", got)
	}
}

// Review focus 5: a member who left is not in the table and reaches nobody.
func TestPatchDropsFormerMembers(t *testing.T) {
	tb := table()
	tb.Members = tb.Members[:1] // bob left
	x, _ := vroute.Patch(base(t), tb, map[string]string{"alice": "ru4", "bob": "ru4"}, "ru4")
	rules, _ := parse(t, x)
	for _, r := range rules {
		if slices.Contains(r.User, "bob") {
			t.Fatalf("bob still in a rule: %+v", r)
		}
	}
}

func TestPatchWithoutTableKeepsConfig(t *testing.T) {
	in := base(t)
	out, err := vroute.Patch(in, nil, nil, "ru4")
	if err != nil || string(out) != string(in) {
		t.Fatal("nil table changed the config")
	}
}

// Every network's resolver is reachable only by that network's members, and
// its rule must come before the block for the whole network prefix.
func TestPatchDeliversDNSQueries(t *testing.T) {
	x, err := vroute.Patch(base(t), table(), map[string]string{"alice": "ru4", "bob": "de2", "eve": "ru4"}, "ru4")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []struct {
				InboundTag, User, IP []string
				Port                 string
				OutboundTag          string
			}
		} `json:"routing"`
		Outbounds []struct {
			Tag      string
			Protocol string
			Settings struct{ Redirect string }
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(x, &cfg); err != nil {
		t.Fatal(err)
	}
	dnsAt, blockAt, n1Seen := -1, -1, false
	for i, r := range cfg.Routing.Rules {
		if slices.Contains(r.IP, nodespec.VDNSAddress) {
			if dnsAt < 0 {
				dnsAt = i
			}
			if r.Port != "53" || !slices.Equal(r.InboundTag, []string{"vpn"}) {
				t.Fatalf("dns rule %+v", r)
			}
			switch r.OutboundTag {
			case vroute.DNSTag("n1"):
				n1Seen = true
				if !slices.Equal(r.User, []string{"alice", "bob"}) {
					t.Fatalf("n1 members %v", r.User)
				}
			case vroute.DNSTag("n2"):
				if !slices.Equal(r.User, []string{"eve"}) {
					t.Fatalf("n2 members %v", r.User)
				}
			default:
				t.Fatalf("unexpected dns rule %+v", r)
			}
		}
		if slices.Contains(r.IP, nodespec.VNetPrefix) {
			blockAt = i
		}
	}
	if !n1Seen || dnsAt < 0 || blockAt < 0 || dnsAt > blockAt {
		t.Fatalf("n1 seen %v, dns rule at %d, block at %d", n1Seen, dnsAt, blockAt)
	}
	var found bool
	for _, o := range cfg.Outbounds {
		if o.Tag == vroute.DNSTag("n1") {
			found = true
			if o.Protocol != "freedom" || o.Settings.Redirect != "127.0.0.1:15353" {
				t.Fatalf("dns outbound %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("no dns outbound")
	}
}
