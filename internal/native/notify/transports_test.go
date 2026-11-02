package notify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
	"github.com/mhsanaei/3x-ui/v3/internal/native/notify"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
)

func TestTelegramPostsToEveryChat(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	tg := &notify.Telegram{API: srv.URL, Token: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", Chats: []string{"1", "-100"}}
	if err := tg.Send(context.Background(), notify.Message{Subject: "cert", Body: "expires"}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/bot123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345/sendMessage" {
		t.Fatalf("paths %v", paths)
	}
	if bodies[0]["chat_id"] != "1" || !strings.Contains(bodies[0]["text"].(string), "expires") {
		t.Fatalf("body %v", bodies[0])
	}
}

func TestTelegramReportsAFailedChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	tg := &notify.Telegram{API: srv.URL, Token: "t", Chats: []string{"1"}}
	if err := tg.Send(context.Background(), notify.Message{Subject: "x"}); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v", err)
	}
}

func TestDiscordPostsTheContent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	d := &notify.Discord{URL: srv.URL}
	if err := d.Send(context.Background(), notify.Message{Subject: "node down", Body: "de2"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["content"].(string), "node down") {
		t.Fatalf("content %v", got)
	}
	mentions := got["allowed_mentions"].(map[string]any)["parse"].([]any)
	if len(mentions) != 0 {
		t.Fatalf("mentions %v", mentions)
	}
}

// fakeSMTP is a minimal server: EHLO, optional AUTH PLAIN, MAIL, RCPT, DATA.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
	auth bool
}

func startSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	say := func(s string) { _, _ = w.WriteString(s + "\r\n"); _ = w.Flush() }
	say("220 fake ESMTP")
	inData := false
	var msg strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.msgs = append(f.msgs, msg.String())
				f.mu.Unlock()
				msg.Reset()
				say("250 ok")
				continue
			}
			msg.WriteString(line)
			msg.WriteString("\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(line, "AUTH PLAIN"):
			f.auth = true
			say("235 ok")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			say("250 ok")
		case line == "DATA":
			inData = true
			say("354 go")
		case line == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestEmailSendsThroughSMTP(t *testing.T) {
	srv := startSMTP(t)
	host, port, _ := net.SplitHostPort(srv.ln.Addr().String())
	_, _ = host, port
	portN := 0
	for _, c := range port {
		portN = portN*10 + int(c-'0')
	}
	e := &notify.Email{
		Host: host, Port: portN, User: "u", Pass: "p",
		From: "fbui@example", To: []string{"a@example", "b@example"},
	}
	if err := e.Send(context.Background(), notify.Message{Subject: "hi", Body: "line1\nline2"}); err != nil {
		t.Fatal(err)
	}
	if !srv.auth {
		t.Fatal("no AUTH")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.msgs) != 1 {
		t.Fatalf("messages %v", srv.msgs)
	}
	m := srv.msgs[0]
	for _, want := range []string{"From: fbui@example", "To: a@example, b@example", "Subject: hi", "line1", "line2"} {
		if !strings.Contains(m, want) {
			t.Fatalf("missing %q in %q", want, m)
		}
	}
}

func TestEmailHeaderCannotInjectALine(t *testing.T) {
	srv := startSMTP(t)
	host, port, _ := net.SplitHostPort(srv.ln.Addr().String())
	portN := 0
	for _, c := range port {
		portN = portN*10 + int(c-'0')
	}
	e := &notify.Email{Host: host, Port: portN, From: "f@x", To: []string{"t@x"}}
	if err := e.Send(context.Background(), notify.Message{Subject: "a\r\nBcc: evil@x"}); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if strings.Contains(strings.ToLower(srv.msgs[0]), "\nbcc:") {
		t.Fatalf("header injection: %q", srv.msgs[0])
	}
}

func TestBuildReadsTheSettings(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	_ = settings.SetMany(ctx, h, settings.Fleet, map[string]string{
		"notify.telegram.enable": "true",
		"notify.telegram.token":  "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345",
		"notify.telegram.chats":  "1",
		"notify.discord.enable":  "true",
		"notify.discord.url":     "https://discord.example/hook",
	})
	got := notify.Build(h, notify.Config{})
	var names []string
	for _, s := range got {
		names = append(names, s.Name())
	}
	if strings.Join(names, ",") != "telegram,discord" {
		t.Fatalf("senders %v", names)
	}
	if !notify.Wants(notify.EventsFor(h, "telegram"), "node.down") {
		t.Fatal("an empty event list must mean all")
	}
	_ = settings.Set(ctx, h, settings.Fleet, "notify.telegram.events", "node.down,cert.expiring")
	if notify.Wants(notify.EventsFor(h, "telegram"), "client.expired") {
		t.Fatal("a listed event set matched a foreign type")
	}
}
