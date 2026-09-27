package service

import (
	"net"
	"slices"
	"strconv"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// nl1 kept reporting a running core for hours after its :443 listener was
// gone; the check has to tell a dead port from a live one and leave UDP alone.
func TestLostListenersFindsAPortNobodyAccepts(t *testing.T) {
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gonePort := gone.Addr().(*net.TCPAddr).Port
	_ = gone.Close()

	ib := func(tag, listen string, port int, protocol, stream string) xray.InboundConfig {
		return xray.InboundConfig{
			Tag: tag, Listen: json_util.RawMessage(`"` + listen + `"`), Port: port, Protocol: protocol,
			Settings: json_util.RawMessage(`{}`), StreamSettings: json_util.RawMessage(stream),
		}
	}
	got := lostListeners([]xray.InboundConfig{
		ib("live-ws", "0.0.0.0", live.Addr().(*net.TCPAddr).Port, "vless", `{"network":"ws"}`),
		ib("lost-ws", "0.0.0.0", gonePort, "vless", `{"network":"ws"}`),
		ib("kcp", "127.0.0.1", gonePort, "vless", `{"network":"kcp"}`),
		ib("wg", "", gonePort, "wireguard", ``),
		ib("socket", "/run/x.sock", 0, "vless", `{"network":"ws"}`),
	})
	if want := []string{"lost-ws 127.0.0.1:" + strconv.Itoa(gonePort)}; !slices.Equal(got, want) {
		t.Fatalf("lost = %q, want %q", got, want)
	}
}
