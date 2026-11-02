package xrayrun

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseOnlineIPs(t *testing.T) {
	got, err := parseOnlineIPs([]byte(`{"users":[
		{"email":"alice","ips":[{"ip":"10.0.9.2","last_seen":1700000000},{"ip":"10.0.9.1","last_seen":1700000100}]},
		{"email":"","ips":[{"ip":"10.0.9.9"}]},
		{"email":"bob","ips":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got["alice"]) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got["alice"][0] != (OnlineIP{Email: "alice", IP: "10.0.9.2", LastSeen: 1700000000}) {
		t.Fatalf("alice %+v", got["alice"])
	}
	if got, err := parseOnlineIPs([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}

func TestParseOnline(t *testing.T) {
	got, err := parseOnline([]byte(`{ "users": [ "user>>>alice>>>online", "user>>>bob>>>online" ] }`))
	if err != nil || !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := parseOnline([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}

func TestParseStatsSumsPerUser(t *testing.T) {
	out := []byte(`{"stat":[
		{"name":"user>>>alice>>>traffic>>>uplink","value":10},
		{"name":"user>>>alice>>>traffic>>>downlink","value":200},
		{"name":"user>>>bob>>>traffic>>>downlink","value":0},
		{"name":"inbound>>>api>>>traffic>>>uplink","value":5},
		{"name":"user>>>a>>>b>>>traffic>>>uplink","value":7}]}`)
	got, err := parseStats(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (UserTraffic{Email: "alice", Up: 10, Down: 200}) {
		t.Fatalf("got %+v", got)
	}
	if got, err := parseStats([]byte("{}\n")); err != nil || len(got) != 0 {
		t.Fatalf("empty stats: %v %v", got, err)
	}
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

func config(api, socks int) []byte {
	return fmt.Appendf(nil, `{
	"api": {"tag": "api", "services": ["StatsService"]},
	"stats": {},
	"policy": {"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true}}},
	"inbounds": [
		{"tag": "api", "listen": "127.0.0.1", "port": %d, "protocol": "tunnel", "settings": {"address": "127.0.0.1"}},
		{"tag": "s", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"auth": "noauth"}}
	],
	"outbounds": [{"protocol": "freedom"}],
	"routing": {"rules": [{"inboundTag": ["api"], "outboundTag": "api"}]}
}`, api, socks)
}

// With a real Xray: a good config runs, a rejected one leaves the running
// config and process alone, and stats can be read.
func TestApplyWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY")
	}
	api := freePort(t)
	r := &Runner{Bin: bin, Config: filepath.Join(t.TempDir(), "config.json"), API: fmt.Sprintf("127.0.0.1:%d", api)}
	defer r.Stop()
	ctx := context.Background()
	good := config(api, freePort(t))
	if err := r.Apply(ctx, good); err != nil {
		t.Fatal(err)
	}
	if !r.Running() {
		t.Fatal("xray not running after apply")
	}
	pid := r.cmd.Process.Pid
	if err := r.Apply(ctx, good); err != nil || r.cmd.Process.Pid != pid {
		t.Fatalf("same config restarted Xray (pid %d -> %d, %v): open tunnels would drop", pid, r.cmd.Process.Pid, err)
	}
	if err := r.Apply(ctx, []byte(`{"inbounds":[{"protocol":"no-such-protocol","port":1}]}`)); err == nil {
		t.Fatal("broken config accepted")
	}
	if cur, _ := os.ReadFile(r.Config); string(cur) != string(good) || !r.Running() {
		t.Fatal("rejected config replaced the running one")
	}
	var err error
	for range 20 {
		if _, err = r.Traffic(ctx); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
}
