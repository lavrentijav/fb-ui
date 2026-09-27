package model

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

// nl1 lost its cascade listener on every master-driven client edit: the
// runtime re-add carried each client's share-link encryption, which the core
// refuses on a VLESS inbound, after the old listener had been closed.
func TestGenXrayInboundConfigDropsClientEncryption(t *testing.T) {
	ib := &Inbound{
		Protocol: VLESS, Port: 12443, Tag: "in-443-tcp",
		Settings: `{"decryption":"none","encryption":"none","clients":[` +
			`{"id":"33333333-3333-3333-3333-333333333333","email":"pq@x","encryption":"mlkem768x25519plus.native.0rtt.AAAA"}]}`,
		StreamSettings: `{"network":"ws"}`,
	}
	cfg := &conf.VLessInboundConfig{}
	if err := json.Unmarshal(ib.GenXrayInboundConfig().Settings, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := cfg.Build(); err != nil {
		t.Fatalf("xray-core refuses the inbound: %v", err)
	}
}
