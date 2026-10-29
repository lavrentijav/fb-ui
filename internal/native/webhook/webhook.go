// Package webhook delivers signed JSON events to an HTTP endpoint. It knows
// nothing about the panel: a caller hands it a target and an event, so the same
// code serves any host that has events to announce.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Event is the body a receiver gets.
type Event struct {
	Type   string    `json:"type"`
	Source string    `json:"source,omitempty"`
	Time   time.Time `json:"time"`
	Data   any       `json:"data,omitempty"`
}

// Target is where and how one delivery goes.
type Target struct {
	URL      string
	Secret   string // empty: the delivery is unsigned
	Delivery string // stable id of the event, sent as X-Fbui-Delivery so a receiver can drop a repeat
}

// Sender posts events. The zero value is not usable; use New.
type Sender struct {
	Client   *http.Client
	Attempts int                 // total tries, at least 1
	Backoff  time.Duration       // wait before the second try, doubling after
	Sleep    func(time.Duration) // waits between tries; time.Sleep by default
}

// New returns a Sender with a short timeout and three tries.
func New(client *http.Client) *Sender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Sender{Client: client, Attempts: 3, Backoff: 2 * time.Second, Sleep: time.Sleep}
}

// Sign is the signature header value: hex HMAC-SHA256 over "<unix>.<body>", so a
// receiver can reject a replayed delivery by its timestamp.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// permanent marks a failure a retry cannot fix (a 4xx other than 408/429).
type permanent struct{ error }

// Send delivers one event, retrying transient failures. It returns the last
// error, or nil once the receiver answered 2xx.
func (s *Sender) Send(ctx context.Context, t Target, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	attempts := max(s.Attempts, 1)
	wait := s.Backoff
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			s.Sleep(wait)
			wait *= 2
		}
		last = s.once(ctx, t, ev.Type, body)
		if last == nil {
			return nil
		}
		var p permanent
		if errors.As(last, &p) {
			return p.error
		}
	}
	return last
}

func (s *Sender) once(ctx context.Context, t Target, typ string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return permanent{err}
	}
	now := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fbui-webhook")
	req.Header.Set("X-Fbui-Event", typ)
	req.Header.Set("X-Fbui-Timestamp", strconv.FormatInt(now, 10))
	if t.Delivery != "" {
		req.Header.Set("X-Fbui-Delivery", t.Delivery)
	}
	if t.Secret != "" {
		req.Header.Set("X-Fbui-Signature", Sign(t.Secret, now, body))
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	default:
		return permanent{fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
}
