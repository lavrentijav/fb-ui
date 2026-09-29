package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var (
	tcpListeningProtocols = []string{"vless", "vmess", "trojan", "shadowsocks", "socks", "http", "mixed", "dokodemo-door", "tunnel"}
	tcpTransports         = []string{"", "tcp", "raw", "ws", "httpupgrade", "grpc", "xhttp", "splithttp"}
)

// listenerProbeAddr is where an inbound should accept TCP, or "" when it
// does not (UDP-only transports, unix sockets).
func listenerProbeAddr(ib xray.InboundConfig) string {
	if ib.Port <= 0 || !slices.Contains(tcpListeningProtocols, ib.Protocol) {
		return ""
	}
	var settings struct {
		Network string `json:"network"`
	}
	_ = json.Unmarshal(ib.Settings, &settings)
	if settings.Network == "udp" {
		return ""
	}
	var stream struct {
		Network     string `json:"network"`
		TlsSettings struct {
			Alpn []string `json:"alpn"`
		} `json:"tlsSettings"`
	}
	_ = json.Unmarshal(ib.StreamSettings, &stream)
	if !slices.Contains(tcpTransports, stream.Network) {
		return ""
	}
	if len(stream.TlsSettings.Alpn) == 1 && stream.TlsSettings.Alpn[0] == "h3" {
		return ""
	}
	var listen string
	_ = json.Unmarshal(ib.Listen, &listen)
	switch {
	case strings.HasPrefix(listen, "/") || strings.HasPrefix(listen, "@"):
		return ""
	case listen == "" || listen == "0.0.0.0":
		listen = "127.0.0.1"
	case listen == "::":
		listen = "::1"
	}
	return net.JoinHostPort(listen, strconv.Itoa(ib.Port))
}

// LostListeners names the inbounds whose TCP port refuses connections: the
// core can drop a listener and still report running. Runtime API edits never
// reach the process config, so the stored local inbounds are checked too.
func (s *XrayService) LostListeners() []string {
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() || process.GetConfig() == nil {
		return nil
	}
	return lostListeners(listenersToCheck(process.GetConfig().InboundConfigs))
}

func listenersToCheck(running []xray.InboundConfig) []xray.InboundConfig {
	inbounds := slices.Clone(running)
	var stored []*model.Inbound
	if err := database.GetDB().Where("enable = ? AND node_id IS NULL", true).Find(&stored).Error; err == nil {
		for _, ib := range stored {
			inbounds = append(inbounds, *ib.GenXrayInboundConfig())
		}
	}
	return inbounds
}

func lostListeners(inbounds []xray.InboundConfig) []string {
	var lost []string
	seen := map[string]bool{}
	for _, ib := range inbounds {
		addr := listenerProbeAddr(ib)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		cancel()
		if err == nil {
			_ = c.Close()
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			lost = append(lost, ib.Tag+" "+addr)
		}
	}
	return lost
}
