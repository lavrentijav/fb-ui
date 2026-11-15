package vnet_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/vnet"
)

func TestClientConfigIsValidXray(t *testing.T) {
	x := os.Getenv("NETLAB_XRAY")
	if x == "" {
		x = "/usr/local/x-ui/bin/xray-linux-amd64"
	}
	if _, err := os.Stat(x); err != nil {
		t.Skip("no xray binary")
	}
	cfg, err := vnet.ClientConfig(
		vnet.User{
			Email: "alice", UUID: "11111111-1111-4111-8111-111111111111",
			RevUUID: "22222222-2222-4222-8222-222222222222", IP: "100.64.0.7",
		},
		vnet.ClientEntry{Address: "ru.example", Port: 443, SNI: "ru.example", Host: "ru.example", Network: "ws", Path: "/ws"},
		[]map[string]any{{"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"from-node"`, `"127.0.0.1:0"`, `"100.64.0.0/10"`} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config lacks %s:\n%s", want, cfg)
		}
	}
	var parsed struct {
		Outbounds []struct {
			Settings struct {
				Reverse struct{ Tag string } `json:"reverse"`
			} `json:"settings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag []string `json:"inboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	_ = json.Unmarshal(cfg, &parsed)
	var tag string
	for _, o := range parsed.Outbounds {
		if o.Settings.Reverse.Tag != "" {
			tag = o.Settings.Reverse.Tag
		}
	}
	if tag == "" || parsed.Routing.Rules[0].InboundTag[0] != tag {
		t.Fatalf("the reverse tag %q and the rule for it differ", tag)
	}
	f := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(f, cfg, 0o600)
	if out, err := exec.Command(x, "run", "-test", "-c", f).CombinedOutput(); err != nil {
		t.Fatalf("xray rejects it: %s", out)
	}
}
