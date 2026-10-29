package webhookmod

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/webhook"
)

// EventKeepDays is how long the leader keeps events.
const EventKeepDays = 7

// tickEvery is how often the leader looks for new events.
var tickEvery = 2 * time.Second

// Run delivers events while this master is the leader.
func (m *Module) Run(ctx context.Context, h module.Host) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = m.tick(ctx, h) // a failed delivery is on the status record and is retried next pass
		}
	}
}

type queued struct {
	key string
	at  int64
	ev  webhook.Event
}

// after reports whether a is later than the position (at, key).
func (a queued) after(at int64, key string) bool {
	return a.at > at || (a.at == at && a.key > key)
}

func (m *Module) events(h module.Host) []queued {
	var out []queued
	for _, key := range h.Keys(module.EventTable) {
		rec, ok := h.Get(module.EventTable, key)
		if !ok {
			continue
		}
		at := num(rec, "at")
		var data json.RawMessage
		if raw, ok := rec["data"]; ok {
			data = raw
		}
		out = append(out, queued{key: key, at: at, ev: webhook.Event{
			Type: str(rec, "type"), Source: str(rec, "source"), Time: time.UnixMilli(at), Data: data,
		}})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].at != out[j].at {
			return out[i].at < out[j].at
		}
		return out[i].key < out[j].key
	})
	return out
}

// tick is one delivery pass. Only the leader acts. Each enabled webhook has a
// cursor, the position of the last event looked at; a webhook seen for the
// first time starts at the newest event, so adding one never replays history.
// A failed delivery stops that webhook's pass at the failed event, so events
// reach a receiver in order and are retried until they go through.
func (m *Module) tick(ctx context.Context, h module.Host) (sent int, err error) {
	if !h.Leader(ctx) {
		return 0, nil
	}
	events := m.events(h)
	m.pruneEvents(ctx, h, events)
	var errs []error
	for _, id := range h.Keys(Table) {
		rec, ok := h.Get(Table, id)
		if !ok || !flag(rec, "enable") {
			continue
		}
		var want []string
		_ = json.Unmarshal(rec["events"], &want)
		cur, have := h.Get(CursorTable, id)
		if !have {
			at, key := h.Now().UnixMilli(), ""
			if n := len(events); n > 0 && events[n-1].at >= at {
				at, key = events[n-1].at, events[n-1].key
			}
			m.setCursor(ctx, h, id, at, key)
			continue
		}
		curAt, curKey := num(cur, "afterAt"), str(cur, "after")
		lastAt, lastKey, moved := curAt, curKey, false
		for _, e := range events {
			if !e.after(curAt, curKey) {
				continue
			}
			if Matches(want, e.ev.Type) {
				if derr := m.Deliver(ctx, h, id, e.ev, e.key); derr != nil {
					errs = append(errs, derr)
					break
				}
				sent++
			}
			lastAt, lastKey, moved = e.at, e.key, true
		}
		if moved {
			m.setCursor(ctx, h, id, lastAt, lastKey)
		}
	}
	return sent, errors.Join(errs...)
}

func (m *Module) setCursor(ctx context.Context, h module.Host, id string, at int64, key string) {
	_, _ = h.Set(ctx, CursorTable, id, map[string]any{"afterAt": at, "after": key})
}

func (m *Module) pruneEvents(ctx context.Context, h module.Host, events []queued) {
	cutoff := h.Now().Add(-EventKeepDays * 24 * time.Hour).UnixMilli()
	for _, e := range events {
		if e.at < cutoff {
			_, _ = h.Delete(ctx, module.EventTable, e.key)
		}
	}
}
