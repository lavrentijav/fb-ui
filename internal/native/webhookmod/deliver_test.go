package webhookmod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []string // "<delivery> <type>" in arrival order
	code int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{code: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.code == http.StatusOK {
			r.got = append(r.got, req.Header.Get("X-Fbui-Delivery")+" "+req.Header.Get("X-Fbui-Event"))
		}
		w.WriteHeader(r.code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func (e *env) at(ms int64) {
	e.h.Clock = func() time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
}

func (e *env) emit(t *testing.T, typ string, atMs int64) {
	t.Helper()
	if err := e.h.Emit(context.Background(), module.Event{Type: typ, At: t0.UnixMilli() + atMs, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) tick(t *testing.T) int {
	t.Helper()
	n, err := e.mod.Tick(context.Background(), e.h)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return n
}

func TestOnlyTheLeaderDelivers(t *testing.T) {
	e := setup(t)
	e.at(0)
	rc := newReceiver(t)
	e.create(t, map[string]any{"name": "a", "url": rc.srv.URL, "enable": true})
	e.h.IsLeader = true
	e.tick(t) // sets the cursor
	e.emit(t, "node.down", 10)
	e.h.IsLeader = false
	if n := e.tick(t); n != 0 || len(rc.seen()) != 0 {
		t.Fatalf("a follower delivered: n=%d seen=%v", n, rc.seen())
	}
	e.h.IsLeader = true
	if n := e.tick(t); n != 1 || len(rc.seen()) != 1 {
		t.Fatalf("the leader did not deliver: n=%d seen=%v", n, rc.seen())
	}
}

func TestEachEventIsSentOnceInOrder(t *testing.T) {
	e := setup(t)
	e.at(0)
	e.h.IsLeader = true
	rc := newReceiver(t)
	e.create(t, map[string]any{"name": "a", "url": rc.srv.URL, "enable": true})
	e.tick(t)
	e.emit(t, "b", 20)
	e.emit(t, "a", 10)
	e.emit(t, "c", 30)
	if n := e.tick(t); n != 3 {
		t.Fatalf("sent %d, want 3", n)
	}
	if n := e.tick(t); n != 0 {
		t.Fatalf("second pass sent %d, want 0", n)
	}
	want := []string{"a", "b", "c"}
	got := rc.seen()
	for i, w := range want {
		if len(got) != 3 || got[i][len(got[i])-1:] != w {
			t.Fatalf("order = %v, want a b c", got)
		}
	}
}

func TestFailureStopsTheCursorAndRetriesNext(t *testing.T) {
	e := setup(t)
	e.at(0)
	e.h.IsLeader = true
	rc := newReceiver(t)
	e.create(t, map[string]any{"name": "a", "url": rc.srv.URL, "enable": true})
	e.tick(t)
	e.emit(t, "one", 10)
	e.emit(t, "two", 20)
	rc.code = http.StatusInternalServerError
	if _, err := e.mod.Tick(context.Background(), e.h); err == nil {
		t.Fatal("a failing receiver must surface an error")
	}
	rc.mu.Lock()
	rc.code = http.StatusOK
	rc.mu.Unlock()
	if n := e.tick(t); n != 2 {
		t.Fatalf("after recovery sent %d, want both events", n)
	}
	got := rc.seen()
	if len(got) != 2 || got[0][len(got[0])-3:] != "one" || got[1][len(got[1])-3:] != "two" {
		t.Fatalf("delivered %v", got)
	}
}

func TestNewWebhookDoesNotReplayHistory(t *testing.T) {
	e := setup(t)
	e.at(100)
	e.h.IsLeader = true
	e.emit(t, "old", 10)
	rc := newReceiver(t)
	e.create(t, map[string]any{"name": "a", "url": rc.srv.URL, "enable": true})
	if n := e.tick(t); n != 0 || len(rc.seen()) != 0 {
		t.Fatalf("history was replayed: %v", rc.seen())
	}
}

func TestFilterDisabledAndEmptyTablesAreHarmless(t *testing.T) {
	e := setup(t)
	e.at(0)
	e.h.IsLeader = true
	if n := e.tick(t); n != 0 {
		t.Fatalf("empty fleet sent %d", n)
	}
	rc := newReceiver(t)
	e.create(t, map[string]any{"name": "only-cert", "url": rc.srv.URL, "enable": true, "events": []string{"cert.expiring"}})
	e.create(t, map[string]any{"name": "off", "url": rc.srv.URL, "enable": false})
	e.tick(t)
	e.emit(t, "node.down", 10)
	e.emit(t, "cert.expiring", 20)
	if n := e.tick(t); n != 1 {
		t.Fatalf("sent %d, want only the cert event to the filtered hook", n)
	}
	// a cursor set far ahead (a skewed clock) must neither crash nor resend
	e.at(10_000_000)
	e.emit(t, "late", 5)
	e.tick(t)
}

func TestOldEventsArePruned(t *testing.T) {
	e := setup(t)
	e.at(0)
	e.h.IsLeader = true
	e.emit(t, "ancient", 0)
	e.at(8 * 24 * 3600 * 1000)
	e.emit(t, "fresh", 8*24*3600*1000)
	e.tick(t)
	if n := len(e.h.Keys(module.EventTable)); n != 1 {
		t.Fatalf("events left = %d, want only the fresh one", n)
	}
}
