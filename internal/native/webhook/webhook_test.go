package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func quick(s *Sender) *Sender { s.Sleep = func(time.Duration) {}; return s }

func TestSendSignsTheBodyAndStopsOnSuccess(t *testing.T) {
	var got struct{ sig, ts, typ, delivery string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.sig, got.ts, got.typ, got.delivery = r.Header.Get("X-Fbui-Signature"), r.Header.Get("X-Fbui-Timestamp"), r.Header.Get("X-Fbui-Event"), r.Header.Get("X-Fbui-Delivery")
	}))
	defer srv.Close()
	err := quick(New(nil)).Send(context.Background(), Target{URL: srv.URL, Secret: "s3", Delivery: "m1:0001"}, Event{Type: "node.down", Time: time.Unix(1, 0)})
	if err != nil || got.typ != "node.down" || got.sig == "" || got.ts == "" || got.delivery != "m1:0001" {
		t.Fatalf("err=%v got=%+v", err, got)
	}
}

func TestSignDependsOnSecretTimeAndBody(t *testing.T) {
	a := Sign("k", 1, []byte("x"))
	if a == Sign("k2", 1, []byte("x")) || a == Sign("k", 2, []byte("x")) || a == Sign("k", 1, []byte("y")) {
		t.Fatal("signature must change with each input")
	}
}

func TestSendRetriesServerErrorsButNotClientErrors(t *testing.T) {
	var hits atomic.Int32
	code := http.StatusBadGateway
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(code)
	}))
	defer srv.Close()
	s := quick(New(nil))
	if err := s.Send(context.Background(), Target{URL: srv.URL}, Event{Type: "x"}); err == nil || hits.Load() != 3 {
		t.Fatalf("a 502 should be tried 3 times, hits=%d err=%v", hits.Load(), err)
	}
	hits.Store(0)
	code = http.StatusBadRequest
	if err := s.Send(context.Background(), Target{URL: srv.URL}, Event{Type: "x"}); err == nil || hits.Load() != 1 {
		t.Fatalf("a 400 must not be retried, hits=%d", hits.Load())
	}
}
