package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// withoutClients answers the Xray config with the named emails removed from
// every inbound's client list. It is how a node stops a client locally (its
// quota is spent, or a master is unreachable): the change goes through the
// same hot-apply path as any other config, so open tunnels of other users are
// not cut.
func withoutClients(raw []byte, emails map[string]bool) ([]byte, error) {
	if len(emails) == 0 {
		return raw, nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agent: config: %w", err)
	}
	var inbounds []map[string]json.RawMessage
	if err := json.Unmarshal(cfg["inbounds"], &inbounds); err != nil {
		return nil, fmt.Errorf("agent: inbounds: %w", err)
	}
	changed := false
	for _, ib := range inbounds {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(ib["settings"], &settings); err != nil {
			continue // not a VLESS inbound
		}
		var clients []map[string]json.RawMessage
		if err := json.Unmarshal(settings["clients"], &clients); err != nil {
			continue
		}
		kept := clients[:0]
		for _, c := range clients {
			var email string
			_ = json.Unmarshal(c["email"], &email)
			if email != "" && emails[email] {
				changed = true
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == len(clients) {
			continue
		}
		next, err := json.Marshal(kept)
		if err != nil {
			return nil, err
		}
		settings["clients"] = next
		if ib["settings"], err = json.Marshal(settings); err != nil {
			return nil, err
		}
	}
	if !changed {
		return raw, nil
	}
	next, err := json.Marshal(inbounds)
	if err != nil {
		return nil, err
	}
	cfg["inbounds"] = next
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(cfg); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
