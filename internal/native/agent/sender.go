package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mhsanaei/3x-ui/v3/internal/native/pick"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
)

// Sender drains the journal to the master pick has bound the node to.
type Sender struct {
	Journal *Journal
	Select  *pick.Selector
	// Dial connects to a master's client port (TLS, wire.NodeInbox prefix).
	Dial    func(ctx context.Context, master string) (*nats.Conn, error)
	Timeout time.Duration // per report (0 = 5s)
	Batch   int           // reports per Flush (0 = 200)

	mu    sync.Mutex
	conns map[string]*nats.Conn
}

// Flush sends pending reports in order and acks what the master stored.
// On a failed send it marks the master failed, so the next Flush rebinds.
func (s *Sender) Flush(ctx context.Context) (int, error) {
	master, err := s.Select.Tick(ctx)
	if master == "" {
		return 0, err
	}
	nc, err := s.conn(ctx, master)
	if err != nil {
		s.Select.Failed(master)
		return 0, err
	}
	timeout, batch := s.Timeout, s.Batch
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if batch <= 0 {
		batch = 200
	}
	var sent int
	var last uint64
	for _, r := range s.Journal.Pending(batch) {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		err = reports.Send(sctx, nc, master, r)
		cancel()
		if err != nil {
			s.Select.Failed(master)
			s.drop(master)
			break
		}
		sent, last = sent+1, r.Seq
	}
	if last > 0 {
		err = errors.Join(err, s.Journal.Ack(last))
	}
	return sent, err
}

// Run flushes every interval until ctx ends.
func (s *Sender) Run(ctx context.Context, every time.Duration, onErr func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	defer s.Close()
	for {
		if _, err := s.Flush(ctx); err != nil && onErr != nil && ctx.Err() == nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Conn is the open connection to master, dialled if needed.
func (s *Sender) Conn(ctx context.Context, master string) (*nats.Conn, error) {
	return s.conn(ctx, master)
}

func (s *Sender) conn(ctx context.Context, master string) (*nats.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nc := s.conns[master]; nc != nil && nc.IsConnected() {
		return nc, nil
	}
	nc, err := s.Dial(ctx, master)
	if err != nil {
		return nil, err
	}
	if s.conns == nil {
		s.conns = map[string]*nats.Conn{}
	}
	s.conns[master] = nc
	return nc, nil
}

func (s *Sender) drop(master string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nc := s.conns[master]; nc != nil {
		nc.Close()
		delete(s.conns, master)
	}
}

// Close drops every master connection.
func (s *Sender) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for m, nc := range s.conns {
		nc.Close()
		delete(s.conns, m)
	}
}
