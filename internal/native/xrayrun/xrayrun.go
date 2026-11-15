// Package xrayrun runs Xray on a node: it swaps in a config only after
// `xray run -test` accepts it, restarts the process, and reads traffic.
package xrayrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	Bin    string // xray binary
	Config string // path of the running config
	// API is the Xray API address for stats (the rendered config must have
	// an api inbound there); empty disables Traffic.
	API string
	// Output receives Xray's stdout and stderr (the agent's log); nil drops it.
	Output io.Writer

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// Apply installs config once `xray run -test` accepts it: live through the API
// when it can, else by a restart; a rejected config leaves Xray untouched.
func (r *Runner) Apply(ctx context.Context, config []byte) error {
	// an unchanged config must not restart Xray: that would cut every tunnel
	if cur, err := os.ReadFile(r.Config); err == nil && bytes.Equal(cur, config) && r.Running() {
		return nil
	}
	dir := filepath.Dir(r.Config)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".next-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(config); err != nil {
		tmp.Close()
		return err
	}
	if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	test := exec.CommandContext(ctx, r.Bin, "run", "-test", "-c", tmp.Name())
	if out, err := test.CombinedOutput(); err != nil {
		// the rejected config is otherwise gone: keep it for the operator
		_ = os.WriteFile(r.Config+".rejected", config, 0o600)
		return fmt.Errorf("xrayrun: config rejected: %s (%w)", lastLine(out), err)
	}
	if cur, err := os.ReadFile(r.Config); err == nil && r.Running() && r.API != "" {
		if plan, ok := planHot(cur, config); ok {
			err := r.applyHot(ctx, plan)
			if err == nil {
				return os.Rename(tmp.Name(), r.Config)
			}
			// a half-applied plan is undone by the restart below
			if r.Output != nil {
				fmt.Fprintf(r.Output, "xrayrun: live update failed, restarting: %v\n", err)
			}
		}
	}
	if err := os.Rename(tmp.Name(), r.Config); err != nil {
		return err
	}
	return r.Restart()
}

// Restart (re)starts Xray with the installed config.
func (r *Runner) Restart() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
	// Xray outlives any request; Stop ends it
	cmd := exec.CommandContext(context.Background(), r.Bin, "run", "-c", r.Config)
	dieWithParent(cmd)
	cmd.Stdout, cmd.Stderr = r.Output, r.Output
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	r.cmd, r.done = cmd, done
	return nil
}

// Running reports whether the Xray process is alive.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *Runner) stopLocked() {
	if r.cmd == nil {
		return
	}
	_ = r.cmd.Process.Signal(os.Interrupt)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		_ = r.cmd.Process.Kill()
		<-r.done
	}
	r.cmd, r.done = nil, nil
}

// UserTraffic is bytes moved by one client since the previous call.
type UserTraffic struct {
	Email    string
	Up, Down int64
}

// Traffic reads and resets per-user counters through the Xray API.
func (r *Runner) Traffic(ctx context.Context) ([]UserTraffic, error) {
	if r.API == "" {
		return nil, errors.New("xrayrun: no API address")
	}
	out, err := exec.CommandContext(ctx, r.Bin, "api", "statsquery", "--server="+r.API, "-pattern", "user>>>", "-reset").Output()
	if err != nil {
		return nil, fmt.Errorf("xrayrun: statsquery: %w", err)
	}
	return parseStats(out)
}

func parseStats(out []byte) ([]UserTraffic, error) {
	var res struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("xrayrun: stats: %w", err)
	}
	byEmail := map[string]*UserTraffic{}
	var order []string
	for _, s := range res.Stat {
		// user>>>EMAIL>>>traffic>>>uplink|downlink
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" || s.Value == 0 {
			continue
		}
		u := byEmail[parts[1]]
		if u == nil {
			u = &UserTraffic{Email: parts[1]}
			byEmail[parts[1]] = u
			order = append(order, parts[1])
		}
		switch parts[3] {
		case "uplink":
			u.Up += s.Value
		case "downlink":
			u.Down += s.Value
		}
	}
	out2 := make([]UserTraffic, 0, len(order))
	for _, e := range order {
		out2 = append(out2, *byEmail[e])
	}
	return out2, nil
}

// OnlineIP is one address a user connected from, and when it was last seen.
type OnlineIP struct {
	Email    string
	IP       string
	LastSeen int64
}

// OnlineIPs lists every user's live addresses (policy statsUserOnline).
func (r *Runner) OnlineIPs(ctx context.Context) (map[string][]OnlineIP, error) {
	if r.API == "" {
		return nil, errors.New("xrayrun: no API address")
	}
	out, err := exec.CommandContext(ctx, r.Bin, "api", "statsonlineiplist", "--server="+r.API, "-all").Output()
	if err != nil {
		return nil, fmt.Errorf("xrayrun: online ips: %w", err)
	}
	return parseOnlineIPs(out)
}

func parseOnlineIPs(out []byte) (map[string][]OnlineIP, error) {
	var res struct {
		Users []struct {
			Email string `json:"email"`
			IPs   []struct {
				IP       string `json:"ip"`
				LastSeen int64  `json:"last_seen"`
			} `json:"ips"`
		} `json:"users"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return map[string][]OnlineIP{}, nil
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("xrayrun: online ips: %w", err)
	}
	got := make(map[string][]OnlineIP, len(res.Users))
	for _, u := range res.Users {
		if u.Email == "" {
			continue
		}
		for _, e := range u.IPs {
			if e.IP != "" {
				got[u.Email] = append(got[u.Email], OnlineIP{Email: u.Email, IP: e.IP, LastSeen: e.LastSeen})
			}
		}
	}
	return got, nil
}

// Online lists the users with a live connection (policy statsUserOnline).
func (r *Runner) Online(ctx context.Context) ([]string, error) {
	if r.API == "" {
		return nil, errors.New("xrayrun: no API address")
	}
	out, err := exec.CommandContext(ctx, r.Bin, "api", "statsgetallonlineusers", "--server="+r.API).Output()
	if err != nil {
		return nil, fmt.Errorf("xrayrun: online: %w", err)
	}
	return parseOnline(out)
}

func parseOnline(out []byte) ([]string, error) {
	var res struct {
		Users []string `json:"users"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("xrayrun: online: %w", err)
	}
	var emails []string
	for _, u := range res.Users {
		if parts := strings.Split(u, ">>>"); len(parts) == 3 {
			emails = append(emails, parts[1])
		}
	}
	return emails, nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
