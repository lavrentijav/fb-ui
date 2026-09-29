// Package replica is one master's copy of the shared state: it stamps local
// writes, keeps them in the op log and exchanges ops with other masters by
// version vector. It knows nothing about the transport; repl carries Pull
// and Ingest over the network.
package replica

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lww"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// wallClock is the host clock; tests replace it.
var wallClock = func() int64 { return time.Now().UnixMilli() }

type Replica struct {
	id    string
	clock *hlc.Clock
	log   *oplog.Log
	state *lww.State
}

// Open replays the log into a fresh state and continues the clock after the
// newest stamp seen, so a restart cannot issue an older stamp.
func Open(id, dir string, opt oplog.Options) (*Replica, error) {
	log, err := oplog.Open(dir, opt)
	if err != nil {
		return nil, err
	}
	r := &Replica{id: id, clock: hlc.NewWithTime(id, func() int64 { return wallClock() }), log: log, state: lww.New()}
	if err := log.Replay(func(op oplog.Op) error {
		r.state.Apply(op)
		r.clock.Observe(op.Stamp)
		return nil
	}); err != nil {
		log.Close()
		return nil, err
	}
	return r, nil
}

func (r *Replica) ID() string        { return r.id }
func (r *Replica) State() *lww.State { return r.state }
func (r *Replica) Close() error      { return r.log.Close() }

// Set writes fields of one record locally; values are marshalled to JSON.
func (r *Replica) Set(table, key string, fields map[string]any) (oplog.Op, error) {
	raw := make(map[string]json.RawMessage, len(fields))
	for name, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return oplog.Op{}, fmt.Errorf("replica: field %s: %w", name, err)
		}
		raw[name] = b
	}
	return r.write(oplog.Op{Table: table, Key: key, Fields: raw})
}

// Delete removes a record everywhere once replicated.
func (r *Replica) Delete(table, key string) (oplog.Op, error) {
	return r.write(oplog.Op{Table: table, Key: key, Delete: true})
}

func (r *Replica) write(op oplog.Op) (oplog.Op, error) {
	op.Origin = r.id
	op.Seq = r.log.Vector()[r.id] + 1
	op.Stamp = r.clock.Now()
	if _, err := r.log.Append(op); err != nil {
		return oplog.Op{}, err
	}
	r.state.Apply(op)
	return op, nil
}

// Vector is what this replica holds; a peer answers it with Pull.
func (r *Replica) Vector() oplog.Vector { return r.log.Vector() }

// Pull returns up to limit ops the holder of v is missing.
func (r *Replica) Pull(v oplog.Vector, limit int) ([]oplog.Op, error) {
	return r.log.Since(v, limit)
}

// Ingest stores and applies ops from a peer. Ops already held are skipped;
// an op that skips a seq stops the batch (the rest is fetched next round).
// It returns how many ops were new.
func (r *Replica) Ingest(ops []oplog.Op) (int, error) {
	n := 0
	for _, op := range ops {
		ok, err := r.log.Append(op)
		if err != nil {
			if errors.Is(err, oplog.ErrGap) {
				return n, nil
			}
			return n, err
		}
		if ok {
			r.state.Apply(op)
			r.clock.Observe(op.Stamp)
			n++
		}
	}
	return n, nil
}

// SyncFrom pulls everything peer has that r lacks, in pages.
func (r *Replica) SyncFrom(peer interface {
	Pull(oplog.Vector, int) ([]oplog.Op, error)
}, page int) (int, error) {
	total := 0
	for {
		ops, err := peer.Pull(r.Vector(), page)
		if err != nil {
			return total, err
		}
		n, err := r.Ingest(ops)
		total += n
		if err != nil || len(ops) < page || n == 0 {
			return total, err
		}
	}
}
