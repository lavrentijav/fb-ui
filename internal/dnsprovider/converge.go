package dnsprovider

import (
	"net/netip"
	"strings"
)

// existingRecord is one record a provider reported for a name and type. ID is
// whatever the provider needs to delete it (Cloudflare: record id; Technitium
// deletes by value, so ID is unused there).
type existingRecord struct {
	ID    string
	Value string
}

// plan returns the records to delete and the values to add so a name ends up
// with exactly want. Records already matching stay untouched, so a converge
// that changes one address of three never drops the other two.
func plan(rtype string, have []existingRecord, want []string) (del []existingRecord, add []string) {
	wanted := make(map[string]bool, len(want))
	order := make([]string, 0, len(want))
	for _, v := range want {
		k := canonicalValue(rtype, v)
		if k == "" || wanted[k] {
			continue
		}
		wanted[k] = true
		order = append(order, strings.TrimSpace(v))
	}
	kept := make(map[string]bool, len(have))
	for _, r := range have {
		k := canonicalValue(rtype, r.Value)
		if wanted[k] && !kept[k] {
			kept[k] = true
			continue
		}
		del = append(del, r)
	}
	for _, v := range order {
		if !kept[canonicalValue(rtype, v)] {
			add = append(add, v)
		}
	}
	return del, add
}

// canonicalValue makes provider-reported and operator-typed values comparable:
// IPv6 has many spellings and Cloudflare echoes TXT content wrapped in quotes.
func canonicalValue(rtype, v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToUpper(rtype) {
	case "A", "AAAA":
		if a, err := netip.ParseAddr(v); err == nil {
			return a.Unmap().String()
		}
	case "TXT":
		return strings.Trim(v, `"`)
	case "CNAME":
		return NormalizeName(v)
	}
	return v
}
