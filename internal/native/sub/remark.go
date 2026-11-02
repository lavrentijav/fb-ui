package sub

import (
	"strconv"
	"strings"
	"time"
)

var timeNow = time.Now
var timeUnix = func(sec int64) time.Time { return time.Unix(sec, 0) }

// Vars is what a remark template can render for one link.
type Vars struct {
	Node      string // the node's name
	Inbound   string // the inbound's tag
	Email     string // the client's email (its identity)
	User      string // the subscription's login
	Protocol  string // vless
	Transport string // ws | xhttp
	Used      int64  // bytes, across the subscription
	Total     int64  // bytes, 0 = unlimited
	Expire    int64  // unix seconds, 0 = never
}

// ExpandRemark renders a remark template. Tokens are {{NAME}}; the supported
// set matches the main line's template where the native model has an
// equivalent (INBOUND, EMAIL, USERNAME, TRAFFIC_USED, TRAFFIC_TOTAL,
// TRAFFIC_LEFT, DAYS_LEFT, EXPIRE_DATE) plus NODE, PROTOCOL and TRANSPORT.
//
// A "|" segment whose tokens all resolve to nothing is dropped, so an absent
// value leaves no dangling separator or decoration; a plain segment (no
// tokens) is always kept.
func ExpandRemark(tmpl string, v Vars) string {
	if tmpl == "" {
		return ""
	}
	if !strings.Contains(tmpl, "{{") {
		return strings.TrimSpace(tmpl)
	}
	segments := strings.Split(tmpl, "|")
	kept := make([]string, 0, len(segments))
	for _, seg := range segments {
		out, hasToken, anyValue := expandSegment(seg, v)
		if hasToken && !anyValue {
			continue
		}
		kept = append(kept, out)
	}
	return strings.TrimSpace(strings.Join(kept, "|"))
}

var remarkTokens = []string{
	"NODE", "INBOUND", "EMAIL", "USERNAME", "USER", "PROTOCOL", "TRANSPORT",
	"TRAFFIC_USED", "TRAFFIC_TOTAL", "TRAFFIC_LEFT", "DAYS_LEFT", "EXPIRE_DATE",
	"STATUS", "STATUS_EMOJI",
}

type part struct {
	literal string
	token   string // "" for a literal part
}

// splitParts breaks a segment into literals and token names.
func splitParts(seg string) []part {
	var out []part
	rest := seg
	for {
		i := strings.Index(rest, "{{")
		j := -1
		if i >= 0 {
			if k := strings.Index(rest[i:], "}}"); k >= 0 {
				j = i + k
			}
		}
		if i < 0 || j < 0 {
			out = append(out, part{literal: rest})
			return out
		}
		if i > 0 {
			out = append(out, part{literal: rest[:i]})
		}
		out = append(out, part{token: strings.ToUpper(strings.TrimSpace(rest[i+2 : j]))})
		rest = rest[j+2:]
	}
}

func expandSegment(seg string, v Vars) (out string, hasToken, anyValue bool) {
	parts := splitParts(seg)
	values := make([]string, len(parts))
	for i, p := range parts {
		if p.token != "" {
			values[i] = value(p.token, v)
			hasToken = true
			if values[i] != "" {
				anyValue = true
			}
		}
	}
	var b strings.Builder
	wroteValue := false
	for i, p := range parts {
		if p.token != "" {
			b.WriteString(values[i])
			wroteValue = wroteValue || values[i] != ""
			continue
		}
		lit := p.literal
		// A hyphen standing alone between two tokens is their separator: it
		// goes when nothing before it produced a value or when the next token
		// is empty, so "{{INBOUND}}-{{EMAIL}}" leaves no leading dash.
		if strings.TrimSpace(lit) == "-" && hasToken {
			next := ""
			for j := i + 1; j < len(parts); j++ {
				if parts[j].token != "" {
					next = values[j]
					break
				}
				if strings.TrimSpace(parts[j].literal) != "" {
					break
				}
			}
			if !wroteValue || next == "" {
				continue
			}
		}
		b.WriteString(lit)
		if strings.TrimSpace(lit) != "" {
			wroteValue = true
		}
	}
	return b.String(), hasToken, anyValue
}

func value(name string, v Vars) string {
	switch name {
	case "NODE":
		return v.Node
	case "INBOUND":
		return v.Inbound
	case "EMAIL", "USERNAME":
		return v.Email
	case "USER":
		return v.User
	case "PROTOCOL":
		return orDefault(v.Protocol, "vless")
	case "TRANSPORT":
		return orDefault(v.Transport, "ws")
	case "TRAFFIC_USED":
		return size(v.Used)
	case "TRAFFIC_TOTAL":
		if v.Total <= 0 {
			return "∞"
		}
		return size(v.Total)
	case "TRAFFIC_LEFT":
		if v.Total <= 0 {
			return "∞"
		}
		left := v.Total - v.Used
		if left < 0 {
			left = 0
		}
		return size(left)
	case "DAYS_LEFT":
		if v.Expire <= 0 {
			return ""
		}
		left := (v.Expire - nowSeconds()) / 86400
		if left < 0 {
			left = 0
		}
		return strconv.FormatInt(left, 10)
	case "EXPIRE_DATE":
		if v.Expire <= 0 {
			return ""
		}
		return formatDate(v.Expire)
	case "STATUS":
		return status(v)
	case "STATUS_EMOJI":
		switch status(v) {
		case "expired":
			return "⛔"
		case "depleted":
			return "🚫"
		}
		return "✅"
	}
	return ""
}

func status(v Vars) string {
	switch {
	case v.Expire > 0 && v.Expire <= nowSeconds():
		return "expired"
	case v.Total > 0 && v.Used >= v.Total:
		return "depleted"
	}
	return "active"
}

// size renders bytes the way a client shows them: one decimal above 1 KiB.
func size(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n)
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	s := strconv.FormatFloat(f, 'f', 1, 64)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".")
	return s + units[i]
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// nowSeconds and formatDate are small seams so a test can pin the clock.
var (
	nowSeconds = func() int64 { return timeNow().Unix() }
	formatDate = func(sec int64) string { return timeUnix(sec).Format("2006-01-02") }
)
