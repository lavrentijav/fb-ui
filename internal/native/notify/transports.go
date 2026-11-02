package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// HTTPDoer is http.Client's Do, so tests can inject a client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func doer(c HTTPDoer) HTTPDoer {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Telegram posts to every configured chat.
type Telegram struct {
	API   string // default https://api.telegram.org
	HTTP  HTTPDoer
	Token string
	Chats []string
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, m Message) error {
	base := t.API
	if base == "" {
		base = "https://api.telegram.org"
	}
	text := strings.TrimSpace(m.Subject + "\n" + m.Body)
	var errs []error
	for _, chat := range t.Chats {
		body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(base, "/")+"/bot"+t.Token+"/sendMessage", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := doer(t.HTTP).Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("telegram %s: %w", chat, err))
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			errs = append(errs, fmt.Errorf("telegram %s: %s %s", chat, resp.Status, strings.TrimSpace(string(raw))))
		}
	}
	return errors.Join(errs...)
}

// Discord posts to one webhook URL.
type Discord struct {
	URL  string
	HTTP HTTPDoer
}

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Send(ctx context.Context, m Message) error {
	payload := map[string]any{
		"content":          strings.TrimSpace("**" + m.Subject + "**\n" + m.Body),
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doer(d.HTTP).Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("discord: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// SMTPDialer opens the connection to an SMTP server; nil uses net.Dialer.
type SMTPDialer func(ctx context.Context, addr string) (net.Conn, error)

// Email sends through SMTP: implicit TLS on 465, STARTTLS elsewhere.
type Email struct {
	Host string
	Port int
	TLS  bool // implicit TLS
	User string
	Pass string
	From string
	To   []string
	Dial SMTPDialer

	// Timeout bounds the whole exchange (default 30 s).
	Timeout time.Duration
}

func (e *Email) Name() string { return "email" }

func (e *Email) port() int {
	if e.Port > 0 {
		return e.Port
	}
	return 587
}

// Send delivers one message. It returns the first error and never hangs past
// the timeout.
func (e *Email) Send(ctx context.Context, m Message) error {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.port()))
	conn, err := e.dial(ctx, addr)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(addr)
	if e.TLS {
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if !e.TLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	if e.User != "" {
		auth := smtp.PlainAuth("", e.User, e.Pass, host)
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(e.From); err != nil {
		return err
	}
	for _, rcpt := range e.To {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(e.compose(m)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (e *Email) dial(ctx context.Context, addr string) (net.Conn, error) {
	if e.Dial != nil {
		return e.Dial(ctx, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// compose builds a plain-text message with the headers SMTP needs.
func (e *Email) compose(m Message) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", e.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(e.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n\r\n")
	body := strings.ReplaceAll(m.Body, "\n", "\r\n")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	b.WriteString(body)
	return []byte(b.String())
}

// sanitizeHeader keeps a header on one line: a newline would start a new one.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
