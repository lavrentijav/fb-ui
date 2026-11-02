// Package notify sends a message through the configured transports: Telegram,
// email (SMTP), or a Discord webhook. Each transport is a small, testable
// client; the module decides what to send.
package notify

import (
	"context"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/settings"
)

// Message is one notification.
type Message struct {
	Subject string
	Body    string
}

// Sender is one transport.
type Sender interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// Config is what Build reads to wire the transports.
type Config struct {
	// TelegramAPI is the bot API base (default https://api.telegram.org);
	// tests point it at a local server.
	TelegramAPI string
	// SMTP overrides the SMTP dial for tests; nil dials host:port.
	SMTP SMTPDialer

	HTTP HTTPDoer
}

// Build returns the enabled transports for the current settings.
func Build(r module.Reader, c Config) []Sender {
	var out []Sender
	if settings.Bool(r, settings.Fleet, "notify.telegram.enable") {
		token := settings.Get(r, settings.Fleet, "notify.telegram.token")
		chats := settings.List(r, settings.Fleet, "notify.telegram.chats")
		if token != "" && len(chats) > 0 {
			out = append(out, &Telegram{API: c.TelegramAPI, HTTP: c.HTTP, Token: token, Chats: chats})
		}
	}
	if settings.Bool(r, settings.Fleet, "notify.email.enable") {
		host := settings.Get(r, settings.Fleet, "notify.email.host")
		to := settings.List(r, settings.Fleet, "notify.email.to")
		if host != "" && len(to) > 0 {
			out = append(out, &Email{
				Host: host, Port: settings.Int(r, settings.Fleet, "notify.email.port"),
				TLS:  settings.Bool(r, settings.Fleet, "notify.email.tls"),
				User: settings.Get(r, settings.Fleet, "notify.email.user"),
				Pass: settings.Get(r, settings.Fleet, "notify.email.pass"),
				From: defaultFrom(settings.Get(r, settings.Fleet, "notify.email.from"), settings.Get(r, settings.Fleet, "notify.email.user")),
				To:   to, Dial: c.SMTP,
			})
		}
	}
	if settings.Bool(r, settings.Fleet, "notify.discord.enable") {
		u := settings.Get(r, settings.Fleet, "notify.discord.url")
		if u != "" {
			out = append(out, &Discord{URL: u, HTTP: c.HTTP})
		}
	}
	return out
}

// EventsFor says which event types a transport wants (empty = all).
func EventsFor(r module.Reader, transport string) []string {
	return settings.List(r, settings.Fleet, "notify."+transport+".events")
}

// Wants reports whether the transport's event list includes typ.
func Wants(events []string, typ string) bool {
	return len(events) == 0 || contains(events, typ)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func defaultFrom(from, user string) string {
	if from != "" {
		return from
	}
	if strings.Contains(user, "@") {
		return user
	}
	return "fbui@localhost"
}

// Render turns an event into a subject and body.
func Render(typ, source string, at int64, data string) Message {
	subject := "fbui: " + typ
	if source != "" {
		subject += " (" + source + ")"
	}
	body := subject
	if data != "" {
		body += "\n\n" + data
	}
	return Message{Subject: subject, Body: body}
}

// HTML escapes the common characters, so a value from an event cannot inject
// markup into an email.
func HTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
