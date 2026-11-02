package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestore"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

// Reporter is where the node keeps reports until a master acks them: the file
// journal for a relay, the micro SQLite for a full node.
type Reporter interface {
	Add(r reports.Report) (reports.Report, error)
	Pending(limit int) []reports.Report
	Ack(seq uint64) error
	Close() error
}

// StoreJournal is the nodestore-backed Reporter. It keeps the same numbering
// guarantee as the file journal -- a sequence never repeats, even after
// acknowledged rows are pruned -- so a master still counts a report once.
type StoreJournal struct {
	store *nodestore.Store
	node  string
	ctx   context.Context
}

// OpenStoreJournal opens the journal on a node store.
func OpenStoreJournal(store *nodestore.Store, node string) *StoreJournal {
	return &StoreJournal{store: store, node: node, ctx: context.Background()}
}

func (j *StoreJournal) Add(r reports.Report) (reports.Report, error) {
	r.Node = j.node
	body, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	seq, err := j.store.AppendReport(j.ctx, time.Now(), body)
	if err != nil {
		return r, err
	}
	r.Seq = seq
	return r, nil
}

func (j *StoreJournal) Pending(limit int) []reports.Report {
	rows, err := j.store.Pending(j.ctx, limit)
	if err != nil {
		return nil
	}
	out := make([]reports.Report, 0, len(rows))
	for _, row := range rows {
		var r reports.Report
		if json.Unmarshal(row.Body, &r) != nil {
			// a corrupt row cannot be delivered; keep the rest moving
			continue
		}
		r.Seq = row.Seq // the store owns the numbering, the body predates it
		out = append(out, r)
	}
	return out
}

func (j *StoreJournal) Ack(seq uint64) error { return j.store.Ack(j.ctx, seq) }

func (j *StoreJournal) Close() error { return nil }

// ErrNoJournal is a guard for callers that must have a journal.
var ErrNoJournal = errors.New("agent: no journal")
