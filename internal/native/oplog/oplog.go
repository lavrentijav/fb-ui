// Package oplog is the replicated change log: each origin numbers its ops
// 1, 2, 3… in its own file, so a version vector says what a peer misses.
package oplog

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
)

// Op is one change to one record. Fields set values (JSON) with the op's
// stamp; Delete hides every field stamped before it.
type Op struct {
	Origin string                     `json:"o"`
	Seq    uint64                     `json:"s"`
	Stamp  hlc.Stamp                  `json:"t"`
	Table  string                     `json:"tb"`
	Key    string                     `json:"k"`
	Fields map[string]json.RawMessage `json:"f,omitempty"`
	Delete bool                       `json:"d,omitempty"`
}

// Vector is origin → last seq held.
type Vector map[string]uint64

func (v Vector) Clone() Vector { return maps.Clone(v) }

var (
	// ErrGap: the op skips seqs of its origin; fetch the missing ones first.
	ErrGap     = errors.New("oplog: op out of order")
	errBadName = errors.New("oplog: bad origin name")
	validName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
)

const maxRecord = 16 << 20

// Log is safe for concurrent use.
type Log struct {
	dir  string
	sync bool

	mu      sync.RWMutex
	files   map[string]*os.File
	offsets map[string][]int64 // offsets[origin][seq-1] = record start
	sizes   map[string]int64
	newest  map[string]hlc.Stamp
}

type Options struct {
	// NoSync skips fsync after each append (tests, bulk import).
	NoSync bool
}

// Open loads every origin file in dir. A torn or corrupt tail — a crash in
// the middle of an append — is cut off; the ops before it stay.
func Open(dir string, opt Options) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Log{
		dir: dir, sync: !opt.NoSync,
		files: map[string]*os.File{}, offsets: map[string][]int64{},
		sizes: map[string]int64{}, newest: map[string]hlc.Stamp{},
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		origin := strings.TrimSuffix(filepath.Base(name), ".log")
		if !validName.MatchString(origin) {
			continue
		}
		if err := l.load(origin); err != nil {
			l.Close()
			return nil, err
		}
	}
	return l, nil
}

func (l *Log) load(origin string) error {
	f, err := os.OpenFile(filepath.Join(l.dir, origin+".log"), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	var offs []int64
	var pos int64
	var newest hlc.Stamp
	r := bufio.NewReader(f)
	for {
		op, n, err := readRecord(r)
		if err != nil || op.Origin != origin || op.Seq != uint64(len(offs))+1 {
			break
		}
		offs = append(offs, pos)
		pos += n
		if op.Stamp.After(newest) {
			newest = op.Stamp
		}
	}
	if err := f.Truncate(pos); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	l.files[origin], l.offsets[origin], l.sizes[origin], l.newest[origin] = f, offs, pos, newest
	return nil
}

func readRecord(r io.Reader) (Op, int64, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Op{}, 0, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n == 0 || n > maxRecord {
		return Op{}, 0, errors.New("oplog: bad record length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Op{}, 0, err
	}
	if crc32.Checksum(body, castagnoli) != binary.BigEndian.Uint32(hdr[4:]) {
		return Op{}, 0, errors.New("oplog: bad checksum")
	}
	var op Op
	if err := json.Unmarshal(body, &op); err != nil {
		return Op{}, 0, err
	}
	return op, int64(8 + n), nil
}

// Append stores op if it is the next one of its origin. An op already held
// is a no-op (false, nil), so re-delivery is harmless; a skipped seq is ErrGap.
func (l *Log) Append(op Op) (bool, error) {
	if !validName.MatchString(op.Origin) {
		return false, errBadName
	}
	body, err := json.Marshal(op)
	if err != nil {
		return false, err
	}
	if len(body) > maxRecord {
		return false, fmt.Errorf("oplog: op of %d bytes is too large", len(body))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	have := uint64(len(l.offsets[op.Origin]))
	switch {
	case op.Seq <= have:
		return false, nil
	case op.Seq != have+1:
		return false, fmt.Errorf("%w: %s has %d, got %d", ErrGap, op.Origin, have, op.Seq)
	}
	f := l.files[op.Origin]
	if f == nil {
		if f, err = os.OpenFile(filepath.Join(l.dir, op.Origin+".log"), os.O_RDWR|os.O_CREATE, 0o600); err != nil {
			return false, err
		}
		l.files[op.Origin] = f
	}
	rec := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(rec[:4], uint32(len(body)))
	binary.BigEndian.PutUint32(rec[4:8], crc32.Checksum(body, castagnoli))
	copy(rec[8:], body)
	start := l.sizes[op.Origin]
	if _, err := f.WriteAt(rec, start); err != nil {
		_ = f.Truncate(start)
		return false, err
	}
	if l.sync {
		if err := f.Sync(); err != nil {
			_ = f.Truncate(start)
			return false, err
		}
	}
	l.offsets[op.Origin] = append(l.offsets[op.Origin], start)
	l.sizes[op.Origin] = start + int64(len(rec))
	if op.Stamp.After(l.newest[op.Origin]) {
		l.newest[op.Origin] = op.Stamp
	}
	return true, nil
}

// Vector is what this log holds.
func (l *Log) Vector() Vector {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v := Vector{}
	for origin, offs := range l.offsets {
		if len(offs) > 0 {
			v[origin] = uint64(len(offs))
		}
	}
	return v
}

// Newest is the highest stamp an origin has issued, for restoring its clock.
func (l *Log) Newest(origin string) hlc.Stamp {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.newest[origin]
}

// Since returns up to limit ops the holder of v is missing, each origin's in
// seq order (origins sorted by name so pages are stable).
func (l *Log) Since(v Vector, limit int) ([]Op, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	origins := slices.Sorted(maps.Keys(l.offsets))
	var out []Op
	for _, origin := range origins {
		offs := l.offsets[origin]
		for seq := v[origin] + 1; seq <= uint64(len(offs)) && len(out) < limit; seq++ {
			op, err := l.readAt(origin, offs[seq-1])
			if err != nil {
				return out, err
			}
			out = append(out, op)
		}
	}
	return out, nil
}

// Replay calls fn with every op, each origin's in order.
func (l *Log) Replay(fn func(Op) error) error {
	ops, err := l.Since(Vector{}, int(^uint(0)>>1))
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := fn(op); err != nil {
			return err
		}
	}
	return nil
}

func (l *Log) readAt(origin string, off int64) (Op, error) {
	op, _, err := readRecord(io.NewSectionReader(l.files[origin], off, l.sizes[origin]-off))
	return op, err
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for _, f := range l.files {
		errs = append(errs, f.Close())
	}
	l.files = map[string]*os.File{}
	return errors.Join(errs...)
}
