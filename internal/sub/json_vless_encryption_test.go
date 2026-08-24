package sub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func updateInboundSettings(t *testing.T, inboundID int, settings string) error {
	t.Helper()
	return database.GetDB().Model(&model.Inbound{}).Where("id = ?", inboundID).Update("settings", settings).Error
}

// Xray refuses to load an outbound whose VLESS user has an empty encryption, so
// a JSON subscription that omits it is unusable in every client.
func TestJsonSubVlessAlwaysCarriesEncryption(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     string
	}{
		{
			name:     "plain inbound stores only decryption",
			settings: `{"clients":[{"id":"11111111-2222-4333-8444-000000004651","email":"enc@e","subId":"enc","enable":true}],"decryption":"none"}`,
			want:     "none",
		},
		{
			name:     "vless encryption mirrors the inbound decryption",
			settings: `{"clients":[{"id":"11111111-2222-4333-8444-000000004652","email":"enc2@e","subId":"enc2","enable":true}],"decryption":"mlkem768x25519plus.native.0rtt.abc"}`,
			want:     "mlkem768x25519plus.native.0rtt.abc",
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedSubDB(t)
			ib := seedSubInbound(t, "enc-sub", "enc", 4651+i, 1, `{"network":"tcp","security":"none"}`)
			if err := updateInboundSettings(t, ib.Id, tt.settings); err != nil {
				t.Fatalf("update settings: %v", err)
			}

			body, _, err := NewSubJsonService("", "", "", NewSubService("")).GetJson("enc-sub", "req.example.com", true)
			if err != nil {
				t.Fatalf("GetJson: %v", err)
			}
			var configs []map[string]any
			if err := json.Unmarshal([]byte(body), &configs); err != nil {
				t.Fatalf("decode subscription: %v (body %.120s)", err, body)
			}
			enc := vlessEncryptionOf(t, configs)
			if enc != tt.want {
				t.Fatalf("outbound encryption = %q, want %q", enc, tt.want)
			}
			if strings.Contains(body, `"encryption": ""`) {
				t.Fatal("subscription still carries an empty encryption")
			}
		})
	}
}

func vlessEncryptionOf(t *testing.T, configs []map[string]any) string {
	t.Helper()
	for _, cfg := range configs {
		outbounds, _ := cfg["outbounds"].([]any)
		for _, raw := range outbounds {
			ob, _ := raw.(map[string]any)
			if ob["protocol"] != "vless" {
				continue
			}
			settings, _ := ob["settings"].(map[string]any)
			enc, _ := settings["encryption"].(string)
			return enc
		}
	}
	t.Fatal("no vless outbound in the subscription")
	return ""
}
