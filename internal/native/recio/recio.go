// Package recio frames records on disk as length + CRC32C + payload, so a
// reader can tell a torn or corrupt tail from good data.
package recio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Max is the largest payload accepted.
const Max = 16 << 20

var (
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
	// ErrCorrupt: bad length or checksum; everything from here on is untrusted.
	ErrCorrupt = errors.New("recio: corrupt record")
)

// Frame returns payload framed for writing.
func Frame(payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > Max {
		return nil, fmt.Errorf("recio: payload of %d bytes", len(payload))
	}
	rec := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(rec[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(rec[4:8], crc32.Checksum(payload, castagnoli))
	copy(rec[8:], payload)
	return rec, nil
}

// Read returns the next payload and the bytes it took on disk.
func Read(r io.Reader) ([]byte, int64, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, 0, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n == 0 || n > Max {
		return nil, 0, ErrCorrupt
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, 0, err
	}
	if crc32.Checksum(body, castagnoli) != binary.BigEndian.Uint32(hdr[4:]) {
		return nil, 0, ErrCorrupt
	}
	return body, int64(8 + n), nil
}
