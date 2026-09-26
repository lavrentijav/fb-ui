// Package edge lets one public port carry the panel, Xray inbounds and other
// local services: TLS connections are routed by SNI without being decrypted,
// and HTTP requests inside the panel's own TLS are routed by host and path.
// It is a leaf package; the panel owns the rules and hands them in.
package edge

import (
	"encoding/binary"
	"errors"
	"io"
	"time"
)

// maxHelloBytes bounds what a client may make us buffer before a decision; a
// real ClientHello with post-quantum key shares stays well below it.
const maxHelloBytes = 64 << 10

var errNotTLS = errors.New("edge: not a TLS handshake")

// Hello is what routing needs from a ClientHello.
type Hello struct {
	SNI  string
	ALPN []string
}

// readHello reads whole TLS records until the ClientHello is complete and
// returns it together with every byte consumed, which must be replayed to the
// chosen backend untouched.
func readHello(r io.Reader, deadline func(time.Time) error, timeout time.Duration) (*Hello, []byte, error) {
	if deadline != nil {
		_ = deadline(time.Now().Add(timeout))
		defer func() { _ = deadline(time.Time{}) }()
	}
	var raw, body []byte
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, raw, err
		}
		raw = append(raw, hdr[:]...)
		if hdr[0] != 0x16 {
			return nil, raw, errNotTLS
		}
		n := int(binary.BigEndian.Uint16(hdr[3:5]))
		if n == 0 || len(raw)+n > maxHelloBytes {
			return nil, raw, errNotTLS
		}
		rec := make([]byte, n)
		if _, err := io.ReadFull(r, rec); err != nil {
			return nil, raw, err
		}
		raw = append(raw, rec...)
		body = append(body, rec...)
		if len(body) < 4 {
			continue
		}
		if body[0] != 0x01 {
			return nil, raw, errNotTLS
		}
		msgLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
		if 4+msgLen > maxHelloBytes {
			return nil, raw, errNotTLS
		}
		if len(body) < 4+msgLen {
			continue
		}
		h, err := parseHello(body[4 : 4+msgLen])
		return h, raw, err
	}
}

type reader struct {
	b   []byte
	err bool
}

func (r *reader) take(n int) []byte {
	if r.err || n < 0 || len(r.b) < n {
		r.err = true
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) u8() int { b := r.take(1); return int(firstOr(b)) }

func (r *reader) u16() int {
	b := r.take(2)
	if len(b) < 2 {
		return 0
	}
	return int(binary.BigEndian.Uint16(b))
}

func firstOr(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

func parseHello(msg []byte) (*Hello, error) {
	r := &reader{b: msg}
	r.take(2 + 32)  // legacy version, random
	r.take(r.u8())  // session id
	r.take(r.u16()) // cipher suites
	r.take(r.u8())  // compression methods
	if r.err {
		return nil, errNotTLS
	}
	h := &Hello{}
	if len(r.b) == 0 {
		return h, nil
	}
	exts := &reader{b: r.take(r.u16())}
	for !exts.err && len(exts.b) >= 4 {
		typ := exts.u16()
		data := &reader{b: exts.take(exts.u16())}
		switch typ {
		case 0x0000: // server_name
			list := &reader{b: data.take(data.u16())}
			for !list.err && len(list.b) >= 3 {
				kind := list.u8()
				name := list.take(list.u16())
				if kind == 0 && !list.err {
					h.SNI = string(name)
				}
			}
		case 0x0010: // application_layer_protocol_negotiation
			list := &reader{b: data.take(data.u16())}
			for !list.err && len(list.b) >= 1 {
				p := list.take(list.u8())
				if !list.err {
					h.ALPN = append(h.ALPN, string(p))
				}
			}
		}
	}
	if r.err || exts.err {
		return nil, errNotTLS
	}
	return h, nil
}
