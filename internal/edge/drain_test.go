package edge

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// lineEcho writes back every line it reads until the client goes away.
func lineEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func echoOnce(c net.Conn, r *bufio.Reader, msg string) error {
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(c, "%s\n", msg); err != nil {
		return err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	if line != msg+"\n" {
		return fmt.Errorf("echo %q, want %q", line, msg)
	}
	return nil
}

func TestDrainRefusesNewTunnelsAndReapsIdleOnes(t *testing.T) {
	drain := NewDrain()
	r := NewRouter(Config{Default: Route{Action: ActionTCP, Target: lineEcho(t)}}, nil)
	r.Drain = drain
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = r.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	dial := func() (net.Conn, *bufio.Reader) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c, bufio.NewReader(c)
	}
	busy, busyR := dial()
	idle, idleR := dial()
	for _, p := range []struct {
		c net.Conn
		r *bufio.Reader
	}{{busy, busyR}, {idle, idleR}} {
		if err := echoOnce(p.c, p.r, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if got := drain.State().Active; got != 2 {
		t.Fatalf("active = %d before drain, want 2", got)
	}

	drain.Set(true, 400*time.Millisecond)
	fresh, freshR := dial()
	if err := echoOnce(fresh, freshR, "new tunnel"); err == nil {
		t.Fatal("a new tunnel was carried while draining")
	}
	for i := range 12 {
		if err := echoOnce(busy, busyR, fmt.Sprint("tick", i)); err != nil {
			t.Fatalf("busy tunnel cut at tick %d: %v", i, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = idle.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := idleR.ReadByte(); err != io.EOF {
		t.Fatalf("idle tunnel read = %v, want EOF", err)
	}
	if st := drain.State(); !st.Enable || st.Active != 1 || st.Since == 0 {
		t.Fatalf("state %+v, want draining with 1 active", st)
	}

	drain.Set(false, 0)
	again, againR := dial()
	if err := echoOnce(again, againR, "back"); err != nil {
		t.Fatalf("tunnel after drain off: %v", err)
	}
}

func TestDrainAnswers503ButKeepsThePanel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "tunnel")
	}))
	t.Cleanup(backend.Close)
	panel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "panel") })
	drain := NewDrain()
	h := NewHTTPRouter(panel, drain)
	if err := h.Update([]HTTPRoute{{PathPrefix: "/ws", Target: backend.URL}}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://node.example.com"+path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get("/ws"); code != 200 || body != "tunnel" {
		t.Fatalf("before drain: %d %q", code, body)
	}
	drain.Set(true, time.Minute)
	if code, _ := get("/ws"); code != http.StatusServiceUnavailable {
		t.Fatalf("draining /ws = %d, want 503", code)
	}
	if code, body := get("/panel/"); code != 200 || body != "panel" {
		t.Fatalf("panel while draining: %d %q", code, body)
	}
}
