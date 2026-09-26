package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/provision/provisiontest"
)

// fakeNodeBox plays a fresh VPS: it answers the preflight probe, swallows
// uploads, pretends to run install.sh and serves the resulting panel API.
type fakeNodeBox struct {
	mu        sync.Mutex
	installed bool
	uploads   map[string]string
	envFile   string
	panel     *httptest.Server
	token     string
	panelPort int
}

func newFakeNodeBox(t *testing.T, installed bool) *fakeNodeBox {
	b := &fakeNodeBox{installed: installed, uploads: map[string]string{}, token: "node-api-token-123456"}
	b.panel = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/basepath/panel/api/server/status" || r.Header.Get("Authorization") != "Bearer "+b.token {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": map[string]any{
			"panelVersion": "v9.9.9", "panelGuid": "guid-node", "xray": map[string]any{"version": "26.1.1", "state": "running"},
		}})
	}))
	t.Cleanup(b.panel.Close)
	u, _ := url.Parse(b.panel.URL)
	b.panelPort, _ = strconv.Atoi(u.Port())
	return b
}

func (b *fakeNodeBox) handle(cmd string, stdin io.Reader, out io.Writer) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "id -u;"):
		state := "fresh"
		if b.installed {
			state = "installed"
		}
		fmt.Fprintf(out, "0\nx86_64\ndebian 12\n%s\n", state)
	case strings.HasPrefix(cmd, "umask 077 && cat > "):
		path := strings.Trim(strings.Fields(cmd)[5], "'")
		data, _ := io.ReadAll(stdin)
		b.uploads[strings.TrimSuffix(path, ".part")] = string(data)
		if strings.HasSuffix(path, "/env.part") {
			b.envFile = string(data)
		}
	case strings.Contains(cmd, "bash ./install.sh"):
		fmt.Fprintf(out, "Installing...\nUsername: generated\nPassword: %s\n", envValue(b.envFile, "XUI_PASSWORD"))
		b.installed = true
	case cmd == "cat /etc/x-ui/install-result.env":
		fmt.Fprintf(out, "XUI_USERNAME=u\nXUI_PANEL_PORT=%d\nXUI_WEB_BASE_PATH=basepath\nXUI_ACCESS_URL=http://127.0.0.1:%d/basepath\nXUI_API_TOKEN=%s\n",
			b.panelPort, b.panelPort, b.token)
	case strings.HasPrefix(cmd, "mkdir -p -m 700"), strings.HasPrefix(cmd, "rm -rf "):
	default:
		fmt.Fprintf(out, "unexpected command %q\n", cmd)
		return 99
	}
	return 0
}

func envValue(env, key string) string {
	for line := range strings.SplitSeq(env, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.Trim(v, "'")
		}
	}
	return ""
}

func waitJob(t *testing.T, s *NodeProvisionService, id string) *ProvisionJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		j, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != "running" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still running: %+v", j.Steps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type recordingDNS struct {
	mu   sync.Mutex
	sets []string
}

func (r *recordingDNS) SetRecords(_ context.Context, name, rtype string, values []string, _ int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets = append(r.sets, name+" "+rtype+" "+strings.Join(values, ","))
	return nil
}
func (r *recordingDNS) DeleteRecords(context.Context, string, string) error { return nil }
func (r *recordingDNS) Check(context.Context) error                         { return nil }

func TestProvisionRegistersANodeAndKeepsSecretsOutOfTheLog(t *testing.T) {
	initCertTestDB(t)
	SetReleaseAssets(ReleaseAssets{InstallScript: []byte("#!/bin/bash\necho install\n")})
	oldDelay := provisionRetryDelay
	provisionRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { provisionRetryDelay = oldDelay })

	box := newFakeNodeBox(t, false)
	srv := provisiontest.Start(t, "root-pw-secret", box.handle)
	var s NodeProvisionService

	job, err := s.Start(&NodeProvisionRequest{
		Name: "msk2", Host: srv.Host, SshPort: srv.Port, SshPassword: "root-pw-secret",
		InstallSource: "github", AllowPrivateAddress: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	j := waitJob(t, &s, job.Id)
	if j.Status != "done" {
		t.Fatalf("job %s: %s\nsteps: %+v\nlog:\n%s", j.Status, j.Error, j.Steps, strings.Join(j.Log, "\n"))
	}

	var nodes []model.Node
	database.GetDB().Find(&nodes)
	if len(nodes) != 1 || nodes[0].Name != "msk2" || nodes[0].Port != box.panelPort || nodes[0].BasePath != "/basepath/" || nodes[0].Scheme != "http" {
		t.Fatalf("registered nodes = %+v", nodes)
	}
	if nodes[0].Guid != "guid-node" || j.NodeId != nodes[0].Id {
		t.Fatalf("verify step did not record the heartbeat: guid=%q jobNode=%d", nodes[0].Guid, j.NodeId)
	}
	if j.HostKey == "" || !strings.HasPrefix(j.HostKey, "SHA256:") {
		t.Fatalf("host key not reported: %q", j.HostKey)
	}

	panelPass := envValue(box.envFile, "XUI_PASSWORD")
	if len(panelPass) < 16 || !strings.Contains(box.envFile, "XUI_SSL_MODE='ip'") || strings.Contains(box.envFile, "XUI_ARCHIVE") {
		t.Fatalf("env file sent to install.sh:\n%s", box.envFile)
	}
	logText := strings.Join(j.Log, "\n")
	for _, secret := range []string{"root-pw-secret", panelPass, box.token} {
		if strings.Contains(logText, secret) || strings.Contains(j.Error, secret) {
			t.Fatalf("secret %q leaked into the job log:\n%s", secret, logText)
		}
	}
	if !strings.Contains(logText, "Password: ******") {
		t.Fatalf("install output not streamed/masked:\n%s", logText)
	}
	if !slices.ContainsFunc(srv.Commands(), func(c string) bool { return strings.HasPrefix(c, "rm -rf /root/.fbui-provision") }) {
		t.Fatal("remote work dir (holding the env file with the panel password) was not removed")
	}
}

func TestProvisionRefusesALivePanel(t *testing.T) {
	initCertTestDB(t)
	SetReleaseAssets(ReleaseAssets{InstallScript: []byte("#!/bin/bash\n")})
	box := newFakeNodeBox(t, true)
	srv := provisiontest.Start(t, "pw", box.handle)
	var s NodeProvisionService

	job, err := s.Start(&NodeProvisionRequest{Name: "de1", Host: srv.Host, SshPort: srv.Port, SshPassword: "pw", AllowPrivateAddress: true})
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, &s, job.Id)
	if j.Status != "failed" || !strings.Contains(j.Error, "refusing to overwrite a live panel") {
		t.Fatalf("status=%s error=%q", j.Status, j.Error)
	}
	for _, c := range srv.Commands() {
		if strings.Contains(c, "install.sh") {
			t.Fatalf("installer touched a box that already runs x-ui: %q", c)
		}
	}
}

func TestProvisionDerivesDomainAndWritesTheRecord(t *testing.T) {
	initCertTestDB(t)
	rec := &recordingDNS{}
	oldFactory := dnsProviderFactory
	dnsProviderFactory = func(dnsprovider.Config) (dnsprovider.Provider, error) { return rec, nil }
	t.Cleanup(func() { dnsProviderFactory = oldFactory })
	prov := addZone(t, "example.com")
	var s NodeProvisionService

	req := &NodeProvisionRequest{Name: "MSK3", Host: "203.0.113.7", SshPassword: "x", DnsProviderId: prov.Id}
	got, err := s.validate(req)
	if err != nil || got.Id != prov.Id || req.Domain != "msk3.example.com" {
		t.Fatalf("validate: prov=%v domain=%q err=%v", got, req.Domain, err)
	}
	run := &provisionRun{job: ProvisionJob{Steps: []ProvisionStep{{Name: stepDNS}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// example.com has real NS, so the visibility wait gives up at the deadline.
	_ = s.stepDNS(ctx, run, req, got)
	if want := []string{"msk3.example.com A 203.0.113.7"}; !slices.Equal(rec.sets, want) {
		t.Fatalf("DNS writes = %v, want %v", rec.sets, want)
	}

	bad := []struct {
		req  NodeProvisionRequest
		want string
	}{
		{NodeProvisionRequest{Name: "bad name", Host: "h", SshPassword: "x", DnsProviderId: prov.Id}, "is not a DNS label"},
		{NodeProvisionRequest{Name: "a", Host: "h", SshPassword: "x", Domain: "a.other.org", DnsProviderId: prov.Id}, "outside zone"},
		{NodeProvisionRequest{Name: "a", Host: "h", SshPassword: "x", DisablePasswordAuth: true}, "only be disabled when the job itself logs in with a key"},
		{NodeProvisionRequest{Name: "a", Host: "h"}, "password or private key is required"},
	}
	for _, tc := range bad {
		if _, err := s.validate(&tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("validate(%+v) err = %v, want %q", tc.req, err, tc.want)
		}
	}
}

func TestParseEnvFileUnquotesPrintfQ(t *testing.T) {
	got := parseEnvFile("XUI_A=plain\nXUI_B='with space'\nXUI_C=$'it\\'s'\nXUI_D=a\\ b\n")
	want := map[string]string{"XUI_A": "plain", "XUI_B": "with space", "XUI_C": "it's", "XUI_D": "a b"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
