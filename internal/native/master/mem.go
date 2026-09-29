package master

import (
	"context"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/lww"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Mem is an in-memory Store over lww.State.
type Mem struct {
	State *lww.State

	mu      sync.Mutex
	applied oplog.Vector
	newest  map[string]hlc.Stamp
}

func NewMem() *Mem {
	return &Mem{State: lww.New(), applied: oplog.Vector{}, newest: map[string]hlc.Stamp{}}
}

func (s *Mem) Apply(_ context.Context, op oplog.Op) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch have := s.applied[op.Origin]; {
	case op.Seq <= have:
		return false, nil
	case op.Seq != have+1:
		return false, oplog.ErrGap
	}
	s.State.Apply(op)
	s.applied[op.Origin] = op.Seq
	if op.Stamp.After(s.newest[op.Origin]) {
		s.newest[op.Origin] = op.Stamp
	}
	return true, nil
}

func (s *Mem) Applied(context.Context) (oplog.Vector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied.Clone(), nil
}

func (s *Mem) Newest(_ context.Context, origin string) (hlc.Stamp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newest[origin], nil
}
