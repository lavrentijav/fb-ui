package sub

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// dedupeLinks drops a link that only repeats an earlier one. Cascade entries
// behind one shared name (ru.walk on four RU nodes) render the same link once
// per node, and a client should see it once. The remark does not count.
func dedupeLinks(links []string) []string {
	seen := make(map[string]struct{}, len(links))
	out := links[:0:0]
	for _, joined := range links {
		var kept []string
		for _, link := range strings.Split(joined, "\n") {
			if link == "" {
				continue
			}
			key := linkIdentity(link)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			kept = append(kept, link)
		}
		if len(kept) > 0 {
			out = append(out, strings.Join(kept, "\n"))
		}
	}
	return out
}

// linkIdentity is a link without its display name: the #fragment, or the
// "ps" field inside a vmess:// JSON payload.
func linkIdentity(link string) string {
	if rest, ok := strings.CutPrefix(link, "vmess://"); ok {
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(rest)
		}
		var doc map[string]any
		if err == nil && json.Unmarshal(raw, &doc) == nil {
			delete(doc, "ps")
			if b, err := json.Marshal(doc); err == nil {
				return "vmess://" + string(b)
			}
		}
	}
	if i := strings.IndexByte(link, '#'); i >= 0 {
		return link[:i]
	}
	return link
}

// dedupeJSONConfigs keeps the first of several configs whose proxy outbound
// is the same; the remarks around it do not matter.
func dedupeJSONConfigs(configs []json_util.RawMessage) []json_util.RawMessage {
	seen := make(map[string]struct{}, len(configs))
	out := configs[:0:0]
	for _, c := range configs {
		if ob := extractProxyOutbound(c); ob != nil {
			if b, err := json.Marshal(ob); err == nil {
				if _, dup := seen[string(b)]; dup {
					continue
				}
				seen[string(b)] = struct{}{}
			}
		}
		out = append(out, c)
	}
	return out
}
