//go:build !linux

package probe

import "net"

func tcpMSS(*net.TCPConn) int { return 0 }
