package xrayrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// hotPlan is how to turn the running config into the next one through the
// Xray API, so connections that nothing changed for stay open.
type hotPlan struct {
	addOutbounds, addInbounds []json.RawMessage
	rmOutbounds, rmInbounds   []string // removed, or changed and re-added
	addUsers                  []json.RawMessage
	rmUsers                   map[string][]string // inbound tag -> emails
	routing                   json.RawMessage     // the whole new routing, nil if unchanged
	goneOutbounds, goneIns    []string            // removed for good, after the routing moved off them
}

func (p hotPlan) empty() bool {
	return len(p.addOutbounds)+len(p.addInbounds)+len(p.rmOutbounds)+len(p.rmInbounds)+
		len(p.addUsers)+len(p.rmUsers)+len(p.goneOutbounds)+len(p.goneIns) == 0 && p.routing == nil
}

type conf map[string]json.RawMessage

func tagOf(raw json.RawMessage) string {
	var t struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(raw, &t)
	return t.Tag
}

func byTag(list []json.RawMessage) (map[string]json.RawMessage, []string) {
	out := map[string]json.RawMessage{}
	var order []string
	for _, raw := range list {
		tag := tagOf(raw)
		out[tag] = raw
		order = append(order, tag)
	}
	return out, order
}

// canonical re-encodes JSON so key order and spacing never count as a change.
func canonical(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// splitClients returns an inbound without its clients, and the clients by email.
func splitClients(raw json.RawMessage) (string, map[string]json.RawMessage, bool) {
	var ib map[string]any
	if json.Unmarshal(raw, &ib) != nil {
		return "", nil, false
	}
	settings, _ := ib["settings"].(map[string]any)
	clients, _ := settings["clients"].([]any)
	byEmail := map[string]json.RawMessage{}
	for _, c := range clients {
		m, _ := c.(map[string]any)
		email, _ := m["email"].(string)
		if email == "" {
			return "", nil, false // no way to remove a user without its email
		}
		b, _ := json.Marshal(m)
		byEmail[email] = b
	}
	if settings != nil {
		delete(settings, "clients")
	}
	rest, _ := json.Marshal(ib)
	return string(rest), byEmail, true
}

// planHot is false when only a restart applies next: the rest is fixed at start,
// the api inbound carries these calls and the first outbound is the default.
func planHot(cur, next []byte) (hotPlan, bool) {
	var a, b conf
	if json.Unmarshal(cur, &a) != nil || json.Unmarshal(next, &b) != nil {
		return hotPlan{}, false
	}
	for _, key := range slices.Sorted(maps.Keys(mergeKeys(a, b))) {
		if key == "inbounds" || key == "outbounds" || key == "routing" {
			continue
		}
		if canonical(a[key]) != canonical(b[key]) {
			return hotPlan{}, false
		}
	}
	var ai, bi, ao, bo []json.RawMessage
	_ = json.Unmarshal(a["inbounds"], &ai)
	_ = json.Unmarshal(b["inbounds"], &bi)
	_ = json.Unmarshal(a["outbounds"], &ao)
	_ = json.Unmarshal(b["outbounds"], &bo)
	if len(ao) == 0 || len(bo) == 0 || canonical(ao[0]) != canonical(bo[0]) {
		return hotPlan{}, false
	}

	var p hotPlan
	curOut, _ := byTag(ao[1:])
	nextOut, nextOutOrder := byTag(bo[1:])
	for _, tag := range nextOutOrder {
		old, ok := curOut[tag]
		switch {
		case !ok:
			p.addOutbounds = append(p.addOutbounds, nextOut[tag])
		case canonical(old) != canonical(nextOut[tag]):
			p.rmOutbounds = append(p.rmOutbounds, tag)
			p.addOutbounds = append(p.addOutbounds, nextOut[tag])
		}
	}
	for tag := range curOut {
		if _, ok := nextOut[tag]; !ok {
			p.goneOutbounds = append(p.goneOutbounds, tag)
		}
	}

	curIn, _ := byTag(ai)
	nextIn, nextInOrder := byTag(bi)
	if canonical(curIn["api"]) != canonical(nextIn["api"]) {
		return hotPlan{}, false
	}
	for _, tag := range nextInOrder {
		if tag == "api" {
			continue
		}
		old, ok := curIn[tag]
		if !ok {
			p.addInbounds = append(p.addInbounds, nextIn[tag])
			continue
		}
		if canonical(old) == canonical(nextIn[tag]) {
			continue
		}
		oldRest, oldClients, ok1 := splitClients(old)
		newRest, newClients, ok2 := splitClients(nextIn[tag])
		if !ok1 || !ok2 || oldRest != newRest {
			p.rmInbounds = append(p.rmInbounds, tag)
			p.addInbounds = append(p.addInbounds, nextIn[tag])
			continue
		}
		// only the clients changed: users come and go, the listener stays
		var added []json.RawMessage
		for _, email := range slices.Sorted(maps.Keys(newClients)) {
			if was, ok := oldClients[email]; ok && canonical(was) == canonical(newClients[email]) {
				continue
			} else if ok {
				p.rmUser(tag, email)
			}
			added = append(added, newClients[email])
		}
		for email := range oldClients {
			if _, ok := newClients[email]; !ok {
				p.rmUser(tag, email)
			}
		}
		if len(added) > 0 {
			p.addUsers = append(p.addUsers, withClients(nextIn[tag], added))
		}
	}
	for tag := range curIn {
		if _, ok := nextIn[tag]; !ok {
			p.goneIns = append(p.goneIns, tag)
		}
	}
	if canonical(a["routing"]) != canonical(b["routing"]) {
		p.routing = b["routing"]
	}
	slices.Sort(p.goneOutbounds)
	slices.Sort(p.goneIns)
	return p, true
}

func mergeKeys(a, b conf) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func (p *hotPlan) rmUser(tag, email string) {
	if p.rmUsers == nil {
		p.rmUsers = map[string][]string{}
	}
	p.rmUsers[tag] = append(p.rmUsers[tag], email)
}

// withClients is the inbound with only these clients, as `xray api adu` wants.
func withClients(raw json.RawMessage, clients []json.RawMessage) json.RawMessage {
	var ib map[string]any
	_ = json.Unmarshal(raw, &ib)
	settings, _ := ib["settings"].(map[string]any)
	list := make([]any, 0, len(clients))
	for _, c := range clients {
		list = append(list, c)
	}
	settings["clients"] = list
	b, _ := json.Marshal(ib)
	return b
}

// applyHot runs the plan through the API: additions first, routing next,
// removals last, so no rule ever points at an outbound that is not there.
func (r *Runner) applyHot(ctx context.Context, p hotPlan) error {
	dir, err := os.MkdirTemp(filepath.Dir(r.Config), ".hot-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := func(name, key string, v any) (string, error) {
		b, err := json.Marshal(map[string]any{key: v})
		if err != nil {
			return "", err
		}
		f := filepath.Join(dir, name)
		return f, os.WriteFile(f, b, 0o600)
	}
	steps := [][]string{}
	if len(p.rmOutbounds) > 0 {
		steps = append(steps, append([]string{"rmo"}, p.rmOutbounds...))
	}
	if len(p.addOutbounds) > 0 {
		f, err := file("ado.json", "outbounds", p.addOutbounds)
		if err != nil {
			return err
		}
		steps = append(steps, []string{"ado", f})
	}
	if len(p.rmInbounds) > 0 {
		steps = append(steps, append([]string{"rmi"}, p.rmInbounds...))
	}
	if len(p.addInbounds) > 0 {
		f, err := file("adi.json", "inbounds", p.addInbounds)
		if err != nil {
			return err
		}
		steps = append(steps, []string{"adi", f})
	}
	for _, tag := range slices.Sorted(maps.Keys(p.rmUsers)) {
		steps = append(steps, append([]string{"rmu", "-tag=" + tag}, p.rmUsers[tag]...))
	}
	if len(p.addUsers) > 0 {
		f, err := file("adu.json", "inbounds", p.addUsers)
		if err != nil {
			return err
		}
		steps = append(steps, []string{"adu", f})
	}
	if p.routing != nil {
		f, err := file("rules.json", "routing", p.routing)
		if err != nil {
			return err
		}
		steps = append(steps, []string{"adrules", f})
	}
	if len(p.goneOutbounds) > 0 {
		steps = append(steps, append([]string{"rmo"}, p.goneOutbounds...))
	}
	if len(p.goneIns) > 0 {
		steps = append(steps, append([]string{"rmi"}, p.goneIns...))
	}
	for _, step := range steps {
		args := append([]string{"api", step[0], "--server=" + r.API}, step[1:]...)
		out, err := exec.CommandContext(ctx, r.Bin, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("xrayrun: api %s: %w: %s", step[0], err, strings.TrimSpace(lastLine(out)))
		}
		if bytes.Contains(bytes.ToLower(out), []byte("failed")) {
			return fmt.Errorf("xrayrun: api %s: %s", step[0], strings.TrimSpace(lastLine(out)))
		}
	}
	return nil
}
