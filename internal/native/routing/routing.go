// Package routing holds the rules of traffic routing that do not depend on the
// panel: validating list entries, matching a target against them, and
// resolving ASNs to prefixes. It moves between the main line and native.
package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// List kinds.
const (
	KindDomain = "domain"
	KindIP     = "ip"
	KindASN    = "asn"
)

// NormalizeASN turns "13335", "as13335" or "AS13335" into "AS13335".
func NormalizeASN(entry string) (string, error) {
	digits := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(entry)), "AS")
	n, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || n == 0 {
		return "", fmt.Errorf("%q is not an AS number (use AS13335)", entry)
	}
	return "AS" + strconv.FormatUint(n, 10), nil
}

// ValidateEntry keeps out values Xray would reject at config load, where the
// failure would be a dead proxy rather than a form error.
func ValidateEntry(kind, entry string) error {
	if kind == KindASN {
		_, err := NormalizeASN(entry)
		return err
	}
	if strings.ContainsAny(entry, " \t\r\n,") {
		return fmt.Errorf("%q must not contain spaces or commas", entry)
	}
	prefix, rest, hasPrefix := strings.Cut(entry, ":")
	if kind == KindIP {
		if hasPrefix && prefix == "geoip" {
			if rest == "" {
				return errors.New("geoip entry needs a category")
			}
			return nil
		}
		if _, err := netip.ParsePrefix(entry); err == nil {
			return nil
		}
		if _, err := netip.ParseAddr(entry); err == nil {
			return nil
		}
		return fmt.Errorf("%q is not an IP, a CIDR or a geoip category", entry)
	}
	if hasPrefix {
		switch prefix {
		case "geosite", "regexp", "domain", "full", "ext":
			if rest == "" {
				return fmt.Errorf("%q needs a value after the prefix", entry)
			}
			return nil
		}
		return fmt.Errorf("%q uses an unknown prefix %s", entry, prefix)
	}
	if strings.Contains(entry, "/") {
		return fmt.Errorf("%q looks like a path, not a domain", entry)
	}
	return nil
}

// Match decides whether target falls under entries of a kind. maybe is set when
// an entry (geosite:, geoip:, ext:) can only be evaluated by the Xray core.
// An ASN list matches through the prefixes it resolved to, so callers pass
// those with KindIP.
func Match(kind, target string, entries []string) (hit, maybe bool) {
	target = strings.ToLower(strings.TrimSpace(target))
	if kind == KindIP || kind == KindASN {
		ip, err := netip.ParseAddr(target)
		if err != nil {
			return false, false
		}
		for _, e := range entries {
			if strings.HasPrefix(e, "geoip:") {
				maybe = true
				continue
			}
			if p, err := netip.ParsePrefix(e); err == nil {
				if p.Contains(ip) {
					return true, maybe
				}
				continue
			}
			if a, err := netip.ParseAddr(e); err == nil && a == ip {
				return true, maybe
			}
		}
		return false, maybe
	}
	for _, e := range entries {
		prefix, rest, has := strings.Cut(e, ":")
		if !has {
			prefix, rest = "", e
		}
		rest = strings.ToLower(rest)
		switch prefix {
		case "geosite", "ext":
			maybe = true
		case "full":
			if target == rest {
				return true, maybe
			}
		case "domain":
			if target == rest || strings.HasSuffix(target, "."+rest) {
				return true, maybe
			}
		case "regexp":
			if re, err := regexp.Compile(rest); err == nil && re.MatchString(target) {
				return true, maybe
			}
		default:
			if strings.Contains(target, rest) {
				return true, maybe
			}
		}
	}
	return false, maybe
}

// Fetcher downloads a URL body.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// ASNPrefixURL is the RIPEstat lookup listing what an AS announces.
const ASNPrefixURL = "https://stat.ripe.net/data/announced-prefixes/data.json?resource="

// ResolveASN returns the announced prefixes of every AS, deduplicated. One
// failing lookup fails the whole call: a half-resolved list would quietly stop
// matching the missing AS, so the caller keeps the previous result instead.
// An AS that announces nothing counts as skipped.
func ResolveASN(ctx context.Context, fetch Fetcher, asns []string) (prefixes []string, skipped int, err error) {
	seen := map[string]struct{}{}
	for _, asn := range asns {
		body, ferr := fetch(ctx, ASNPrefixURL+url.QueryEscape(asn))
		if ferr != nil {
			return nil, 0, fmt.Errorf("%s: %w", asn, ferr)
		}
		var resp struct {
			Data struct {
				Prefixes []struct {
					Prefix string `json:"prefix"`
				} `json:"prefixes"`
			} `json:"data"`
		}
		if jerr := json.Unmarshal(body, &resp); jerr != nil {
			return nil, 0, fmt.Errorf("%s: unreadable answer: %w", asn, jerr)
		}
		found := 0
		for _, p := range resp.Data.Prefixes {
			if _, _, perr := net.ParseCIDR(p.Prefix); perr != nil {
				continue
			}
			found++
			if _, dup := seen[p.Prefix]; dup {
				continue
			}
			seen[p.Prefix] = struct{}{}
			prefixes = append(prefixes, p.Prefix)
		}
		if found == 0 {
			skipped++
		}
	}
	return prefixes, skipped, nil
}

// ParseList reads the common published shapes: one entry per line, hosts files
// ("0.0.0.0 ads.example.com") and adblock ("||ads.example.com^"). Comments are
// skipped and a line Xray would reject is counted, not fatal.
func ParseList(kind string, body []byte) (entries []string, skipped int) {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "[") {
			continue
		}
		if kind == KindDomain {
			if f := strings.Fields(line); len(f) >= 2 && (f[0] == "0.0.0.0" || f[0] == "127.0.0.1" || f[0] == "::" || f[0] == "::1") {
				line = f[1]
			}
			if rest, ok := strings.CutPrefix(line, "||"); ok {
				line = strings.TrimSuffix(strings.SplitN(rest, "^", 2)[0], ".")
			}
			if !strings.Contains(line, ":") {
				line = strings.ToLower(strings.TrimSuffix(line, "."))
			}
			if line == "localhost" || line == "localhost.localdomain" {
				continue
			}
		}
		if ValidateEntry(kind, line) != nil {
			skipped++
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		entries = append(entries, line)
	}
	return entries, skipped
}
