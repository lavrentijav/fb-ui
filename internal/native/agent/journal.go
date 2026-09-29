// Package agent is the node side (fbnode): it journals reports on disk and
// sends them to the master it is bound to, surviving restarts and failover.
package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/recio"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

// Journal numbers reports 1, 2, 3… for good and keeps them until a master
// has acked them; the numbering is what lets masters count a report once.
type Journal struct {
	dir  string
	node string

	mu      sync.Mutex
	f       *os.File
	size    int64
	acked   uint64
	next    uint64
	pending []reports.Report
}

// OpenJournal loads dir; a torn tail from a crash mid-append is cut off.
func OpenJournal(dir, node string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &Journal{dir: dir, node: node}
	if b, err := os.ReadFile(j.ackPath()); err == nil {
		if j.acked, err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err != nil {
			return nil, fmt.Errorf("agent: bad ack file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "reports.log"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	j.f, j.next = f, j.acked+1
	r := bufio.NewReader(f)
	for {
		body, n, err := recio.Read(r)
		if err != nil {
			break
		}
		var rep reports.Report
		if json.Unmarshal(body, &rep) != nil || (j.size > 0 && rep.Seq != j.next) {
			break
		}
		j.size += n
		j.next = rep.Seq + 1
		if rep.Seq > j.acked {
			j.pending = append(j.pending, rep)
		}
	}
	if err := f.Truncate(j.size); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) ackPath() string { return filepath.Join(j.dir, "acked") }

// Add stamps r with this node and the next seq and stores it durably.
func (j *Journal) Add(r reports.Report) (reports.Report, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r.Node, r.Seq = j.node, j.next
	body, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	rec, err := recio.Frame(body)
	if err != nil {
		return r, err
	}
	if _, err := j.f.WriteAt(rec, j.size); err != nil {
		_ = j.f.Truncate(j.size)
		return r, err
	}
	if err := j.f.Sync(); err != nil {
		_ = j.f.Truncate(j.size)
		return r, err
	}
	j.size += int64(len(rec))
	j.next++
	j.pending = append(j.pending, r)
	return r, nil
}

// Pending returns up to limit unacked reports, oldest first.
func (j *Journal) Pending(limit int) []reports.Report {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]reports.Report(nil), j.pending[:min(limit, len(j.pending))]...)
}

// Ack records that every report up to seq reached a master; once nothing is
// pending the file is emptied, the ack file keeps the numbering.
func (j *Journal) Ack(seq uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if seq <= j.acked {
		return nil
	}
	if seq >= j.next {
		return fmt.Errorf("agent: ack %d beyond last report %d", seq, j.next-1)
	}
	tmp := j.ackPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(seq, 10)), 0o600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, j.ackPath()); err != nil {
		return err
	}
	j.acked = seq
	for len(j.pending) > 0 && j.pending[0].Seq <= seq {
		j.pending = j.pending[1:]
	}
	if len(j.pending) == 0 && j.size > 0 {
		if err := j.f.Truncate(0); err != nil {
			return err
		}
		j.size = 0
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (j *Journal) Close() error { return j.f.Close() }
