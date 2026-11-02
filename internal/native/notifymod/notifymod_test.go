package notifymod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/notify"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
)

// receiver records the notifications a transport posted.
type receiver struct {
	mu   sync.Mutex
	got  []string
	fail bool
}

func (r *receiver) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Content string `json:"content"`
			Text    string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Header: http.Header{}}, nil
		}
		if body.Content != "" {
			r.got = append(r.got, body.Content)
		} else {
			r.got = append(r.got, body.Text)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func emit(t *testing.T, h *modtest.Host, typ string, data string) {
	t.Helper()
	raw := json.RawMessage(data)
	if err := h.Emit(context.Background(), module.Event{Type: typ, Source: "de2", At: h.Now().UnixMilli(), Data: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaderDeliversEventsOnce(t *testing.T) {
	h := modtest.NewHost("m1")
	h.IsLeader = true
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	recv := &receiver{}
	_ = settings.SetMany(context.Background(), h, settings.Fleet, map[string]string{
		"notify.discord.enable": "true",
		"notify.discord.url":    "https://discord.example/hook",
	})
	m := &Module{Config: notify.Config{HTTP: recv.client()}}
	ctx := context.Background()

	// the first pass arms cursors at the newest event, so old events are not replayed
	emit(t, h, "node.down", `{"node":"old"}`)
	m.tick(ctx, h)
	if recv.count() != 0 {
		t.Fatalf("history was replayed: %v", recv.got)
	}

	emit(t, h, "node.down", `{"node":"de2"}`)
	m.tick(ctx, h)
	m.tick(ctx, h)
	if recv.count() != 1 {
		t.Fatalf("deliveries %v", recv.got)
	}
	if got := recv.got[0]; got[len(got)-1] != '}' || !contains(got, "node.down") {
		t.Fatalf("message %q", got)
	}

	// a non-leader sends nothing
	h.IsLeader = false
	emit(t, h, "node.up", `{}`)
	m.tick(ctx, h)
	if recv.count() != 1 {
		t.Fatalf("a follower sent: %v", recv.got)
	}
}

func TestEventFilterAndRetry(t *testing.T) {
	h := modtest.NewHost("m1")
	h.IsLeader = true
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	recv := &receiver{}
	_ = settings.SetMany(context.Background(), h, settings.Fleet, map[string]string{
		"notify.discord.enable": "true",
		"notify.discord.url":    "https://discord.example/hook",
		"notify.discord.events": "cert.expiring",
	})
	m := &Module{Config: notify.Config{HTTP: recv.client()}}
	ctx := context.Background()
	m.tick(ctx, h) // arm

	emit(t, h, "node.down", `{}`)
	m.tick(ctx, h)
	if recv.count() != 0 {
		t.Fatalf("a filtered-out event was sent: %v", recv.got)
	}

	recv.fail = true
	emit(t, h, "cert.expiring", `{"name":"x"}`)
	m.tick(ctx, h)
	if recv.count() != 0 {
		t.Fatal("a failed send counted as delivered")
	}
	// a failure did not move the cursor past the event, so it retries
	recv.fail = false
	m.tick(ctx, h)
	if recv.count() != 1 {
		t.Fatalf("the retry did not deliver: %v", recv.got)
	}
	// and only once
	m.tick(ctx, h)
	if recv.count() != 1 {
		t.Fatalf("delivered twice: %v", recv.got)
	}
	if h.Keys(StatusTable) == nil {
		t.Fatal("no status recorded")
	}
}

func TestTestEndpoint(t *testing.T) {
	h := modtest.NewHost("m1")
	h.Clock = func() time.Time { return time.Unix(1_700_000_000, 0) }
	recv := &receiver{}
	_ = settings.SetMany(context.Background(), h, settings.Fleet, map[string]string{
		"notify.discord.enable": "true",
		"notify.discord.url":    "https://discord.example/hook",
	})
	m := &Module{Config: notify.Config{HTTP: recv.client()}}
	r := newRouter(t)
	m.Mount(r, h)
	body := ""
	if code, _ := r.call(t, "POST", "/test/discord", body); code != http.StatusOK {
		t.Fatalf("test: %d", code)
	}
	if recv.count() != 1 {
		t.Fatalf("got %v", recv.got)
	}
	if code, _ := r.call(t, "POST", "/test/email", body); code != http.StatusNotFound {
		t.Fatalf("a disabled transport: %d", code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// router is the smallest module.Router for these tests.
type router struct {
	mux *http.ServeMux
}

func newRouter(t *testing.T) *router { return &router{mux: http.NewServeMux()} }

func (r *router) Handle(method, pattern string, scope module.Scope, h http.HandlerFunc) {
	r.mux.HandleFunc(method+" "+pattern, h)
}

func (r *router) call(t *testing.T, method, path, body string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec
}
