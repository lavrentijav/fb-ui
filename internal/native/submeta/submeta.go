// Package submeta is what a subscription response tells a client besides the
// links themselves: quota, a title, an announcement, alternate hosts and the
// per-server breakdown. It is pure so the header/body shapes are unit-tested;
// the master decides the values.
//
// The set is deliberately larger than any one client reads. Unknown headers
// and unknown query parameters are ignored by every client surveyed (Happ,
// v2RayTun, v2rayN, v2rayNG, Hiddify, Flamix), so sending the superset costs
// a few hundred bytes and lets a client grow into a field without the panel
// changing.
package submeta

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Quota is one subscription-userinfo value.
type Quota struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`  // 0 = unlimited
	Expire   int64 `json:"expire"` // unix seconds, 0 = never
}

// String renders the de-facto `subscription-userinfo` form.
func (q Quota) String() string {
	return "upload=" + i64(q.Upload) +
		"; download=" + i64(q.Download) +
		"; total=" + i64(q.Total) +
		"; expire=" + i64(q.Expire)
}

// Used is the traffic counted so far.
func (q Quota) Used() int64 { return q.Upload + q.Download }

// Announce is the banner several clients show above the server list. Happ and
// v2RayTun read the keywords, others take the text.
type Announce struct {
	Text       string
	URL        string // v2RayTun: opened when the banner is tapped
	Color      string // red | blue | green | #RRGGBB
	ButtonText string
	ButtonLink string
}

// Info is everything the response can say about a subscription.
type Info struct {
	Title       string
	UpdateHours int      // profile-update-interval
	SupportURL  string   // support-url
	WebPageURL  string   // profile-web-page-url
	Announce    Announce // announce / announce-url / sub-info-*
	Quota       Quota
	RefillDate  int64    // subscription-refill-date, unix seconds
	ProviderID  string   // providerid (Happ)
	BackupURLs  []string // profile-backup-urls (Flamix; fallback-url is its alias)
	// Domains and IPs are the sibling panels a client may retry when the
	// primary host stops answering (cluster and Flamix extensions).
	Domains []string
	IPs     []string
	// HwidLimit is the device cap for this subscription (Remnawave header).
	HwidLimit int
	// HideSettings hides the client's own settings screen (Happ).
	HideSettings bool
	// Signature is a hex signature over the body (cluster extension).
	Signature string
	// Servers is the per-server quota (Flamix extension), keyed by the
	// remark the link carries, so a client can show each server's share.
	Servers map[string]Quota
}

// b64 is the `base64:` form Happ uses for values a header cannot carry
// verbatim; clients that do not understand the prefix show the raw text.
func b64(s string) string {
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(s))
}

// Headers is the response header set. A value that is empty is omitted, so a
// minimal subscription sends only subscription-userinfo and the title.
func (in Info) Headers() map[string]string {
	h := map[string]string{}
	// Always sent: every client reads it, and total=0/expire=0 mean unlimited.
	h["Subscription-Userinfo"] = in.Quota.String()
	if in.UpdateHours > 0 {
		h["Profile-Update-Interval"] = strconv.Itoa(in.UpdateHours)
	}
	if in.Title != "" {
		h["Profile-Title"] = b64(in.Title)
		h["Content-Disposition"] = `attachment; filename*=UTF-8''` + pathEscape(in.Title)
	}
	if in.SupportURL != "" {
		h["Support-Url"] = in.SupportURL
	}
	if in.WebPageURL != "" {
		h["Profile-Web-Page-Url"] = in.WebPageURL
	}
	if in.Announce.Text != "" {
		h["Announce"] = b64(in.Announce.Text)
		h["Sub-Info-Text"] = in.Announce.Text
		h["Sub-Info-Color"] = color(in.Announce.Color)
		if in.Announce.URL != "" {
			h["Announce-Url"] = in.Announce.URL
		}
		if in.Announce.ButtonText != "" {
			h["Sub-Info-Button-Text"] = in.Announce.ButtonText
		}
		if in.Announce.ButtonLink != "" {
			h["Sub-Info-Button-Link"] = in.Announce.ButtonLink
		}
	}
	if in.RefillDate > 0 {
		h["Subscription-Refill-Date"] = strconv.FormatInt(in.RefillDate, 10)
	}
	if in.ProviderID != "" {
		h["ProviderID"] = in.ProviderID
	}
	if urls := join(in.BackupURLs); urls != "" {
		h["Profile-Backup-Urls"] = urls
		h["Fallback-Url"] = urls // Happ's name for the same thing
	}
	if d := join(in.Domains); d != "" {
		h["X-Subscription-Fallback-Domains"] = d
	}
	if ips := join(in.IPs); ips != "" {
		h["X-Subscription-Fallback-IPs"] = ips
	}
	if in.HwidLimit > 0 {
		h["X-Hwid-Limit"] = strconv.Itoa(in.HwidLimit)
	}
	if in.HideSettings {
		h["Hide-Settings"] = "1"
	}
	if in.Signature != "" {
		h["X-Subscription-Signature"] = in.Signature
	}
	if s := in.ServerHeader(); s != "" {
		h["Server-Userinfo"] = s
	}
	return h
}

// color normalizes the banner colour Happ accepts; anything else is passed
// through so a client that knows more colours can use it.
func color(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "", "primary", "info":
		return "blue"
	case "success":
		return "green"
	case "warning", "danger":
		return "red"
	}
	return strings.TrimSpace(c)
}

// ServerHeader renders the per-server quota as Flamix reads it:
// `base64:<json>` mapping the link remark to a subscription-userinfo string.
func (in Info) ServerHeader() string {
	if len(in.Servers) == 0 {
		return ""
	}
	obj := make(map[string]string, len(in.Servers))
	for name, q := range in.Servers {
		obj[name] = q.String()
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return "base64:" + base64.StdEncoding.EncodeToString(b)
}

// BodyLines renders the same metadata as Happ-style `#key: value` lines, for
// clients that read a plain body and for providers whose CDN strips headers.
func (in Info) BodyLines() []string {
	var out []string
	add := func(k, v string) {
		if v = sanitizeLine(v); v != "" {
			out = append(out, "#"+k+": "+v)
		}
	}
	if in.Title != "" {
		add("profile-title", b64(in.Title))
	}
	if in.UpdateHours > 0 {
		add("profile-update-interval", strconv.Itoa(in.UpdateHours))
	}
	add("subscription-userinfo", in.Quota.String())
	if in.SupportURL != "" {
		add("support-url", in.SupportURL)
	}
	if in.WebPageURL != "" {
		add("profile-web-page-url", in.WebPageURL)
	}
	if in.Announce.Text != "" {
		add("announce", b64(in.Announce.Text))
		if in.Announce.URL != "" {
			add("announce-url", in.Announce.URL)
		}
		add("sub-info-color", color(in.Announce.Color))
		add("sub-info-text", in.Announce.Text)
		if in.Announce.ButtonText != "" {
			add("sub-info-button-text", in.Announce.ButtonText)
		}
		if in.Announce.ButtonLink != "" {
			add("sub-info-button-link", in.Announce.ButtonLink)
		}
	}
	if in.RefillDate > 0 {
		add("subscription-refill-date", strconv.FormatInt(in.RefillDate, 10))
	}
	if in.ProviderID != "" {
		add("providerid", in.ProviderID)
	}
	if urls := join(in.BackupURLs); urls != "" {
		add("profile-backup-urls", urls)
	}
	if d := join(in.Domains); d != "" {
		add("x-subscription-fallback-domains", d)
	}
	if ips := join(in.IPs); ips != "" {
		add("x-subscription-fallback-ips", ips)
	}
	if in.HwidLimit > 0 {
		add("x-hwid-limit", strconv.Itoa(in.HwidLimit))
	}
	return out
}

// Plain is a subscription body a client reads as a list: the metadata lines,
// then one share link per line. Every surveyed client ignores the # lines.
func (in Info) Plain(links []string) string {
	lines := append(in.BodyLines(), links...)
	return strings.Join(lines, "\n") + "\n"
}

// sanitizeLine keeps one metadata value from breaking the line structure.
func sanitizeLine(v string) string {
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " ")
	return strings.TrimSpace(v)
}

// join renders a comma-separated, deduplicated list in input order.
func join(values []string) string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ", ")
}

// pathEscape escapes a file name for Content-Disposition, keeping it readable.
func pathEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// SortedRemarks is a stable order for tests and for building Server-Userinfo
// from link remarks.
func SortedRemarks(servers map[string]Quota) []string {
	out := make([]string, 0, len(servers))
	for name := range servers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
