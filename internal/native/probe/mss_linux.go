package probe

import (
	"net"

	"golang.org/x/sys/unix"
)

// tcpMSS is the negotiated segment size, which path MTU discovery lowers.
func tcpMSS(c *net.TCPConn) int {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	mss := 0
	_ = raw.Control(func(fd uintptr) {
		mss, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG)
	})
	return mss
}
