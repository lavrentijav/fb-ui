package edge

import (
	"encoding/binary"
	"fmt"
	"net"
)

var proxyV2Sig = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

// proxyHeader tells the backend who the client really is; Xray reads it with
// acceptProxyProtocol, so IP limits and logs keep the real address.
func proxyHeader(version int, src, dst net.Addr) []byte {
	s, sok := src.(*net.TCPAddr)
	d, dok := dst.(*net.TCPAddr)
	if !sok || !dok {
		if version == 1 {
			return []byte("PROXY UNKNOWN\r\n")
		}
		return append(append([]byte{}, proxyV2Sig...), 0x20, 0x00, 0x00, 0x00)
	}
	s4, d4 := s.IP.To4(), d.IP.To4()
	v4 := s4 != nil && d4 != nil
	if version == 1 {
		fam := "TCP6"
		sip, dip := s.IP.String(), d.IP.String()
		if v4 {
			fam, sip, dip = "TCP4", s4.String(), d4.String()
		}
		return fmt.Appendf(nil, "PROXY %s %s %s %d %d\r\n", fam, sip, dip, s.Port, d.Port)
	}
	out := append([]byte{}, proxyV2Sig...)
	out = append(out, 0x21) // v2, PROXY command
	var addrs []byte
	if v4 {
		out = append(out, 0x11) // AF_INET, STREAM
		addrs = append(append(addrs, s4...), d4...)
	} else {
		out = append(out, 0x21) // AF_INET6, STREAM
		addrs = append(append(addrs, s.IP.To16()...), d.IP.To16()...)
	}
	addrs = binary.BigEndian.AppendUint16(addrs, uint16(s.Port))
	addrs = binary.BigEndian.AppendUint16(addrs, uint16(d.Port))
	out = binary.BigEndian.AppendUint16(out, uint16(len(addrs)))
	return append(out, addrs...)
}
