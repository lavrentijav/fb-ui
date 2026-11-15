package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/importpanel"
)

const importFixtureUUID = "11111111-1111-1111-1111-111111111111"

func TestImportRedactsIdentifiersByDefault(t *testing.T) {
	var out, errBuf strings.Builder
	if err := runImport([]string{"-db", importFixture(t)}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), importFixtureUUID) || strings.Contains(out.String(), `"s1"`) {
		t.Fatalf("the default report leaked an identifier:\n%s", out.String())
	}
	var rep importpanel.Report
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("report: %v\n%s", err, out.String())
	}
	if len(rep.Clients) != 1 || !strings.Contains(rep.Clients[0].UUID, "...") {
		t.Fatalf("clients %+v", rep.Clients)
	}
	if rep.Schema == nil || rep.Schema.Style != "client-records" {
		t.Fatalf("schema %+v", rep.Schema)
	}

	out.Reset()
	if err := runImport([]string{"-db", importFixture(t), "-with-secrets"}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), importFixtureUUID) || !strings.Contains(out.String(), `"s1"`) {
		t.Fatalf("-with-secrets did not show the identifiers:\n%s", out.String())
	}
}

func TestImportMarkdownReport(t *testing.T) {
	var out, errBuf strings.Builder
	if err := runImport([]string{"-db", importFixture(t), "-report", "markdown"}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Import plan", "## Detected schema", "## Inbounds", "## Clients", "## Groups", "friends: alice"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%q missing from:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), importFixtureUUID) {
		t.Fatalf("markdown leaked the UUID:\n%s", out.String())
	}
}

func TestImportSchemaPrintsDetectionAndExits(t *testing.T) {
	var out, errBuf strings.Builder
	if err := runImport([]string{"-db", importFixture(t), "-schema"}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	var s importpanel.Schema
	if err := json.Unmarshal([]byte(out.String()), &s); err != nil {
		t.Fatalf("schema output: %v\n%s", err, out.String())
	}
	if s.Style != "client-records" || s.Inbounds != 1 || s.Clients != 1 || s.ClientInbounds != 1 {
		t.Fatalf("schema %+v", s)
	}
	if !strings.Contains(strings.Join(s.Tables, ","), "clients") {
		t.Fatalf("tables %v", s.Tables)
	}
}

// A rerun against an existing fleet is a no-op: every create answers 409 and
// the importer keeps the existing record without failing.
func TestImportApplyIsIdempotentOnConflict(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests++
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/inbounds/") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/sub/alice" {
			_, _ = w.Write([]byte(`{"token":"primary"}`))
			return
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	for run := 1; run <= 2; run++ {
		var out, errBuf strings.Builder
		err := runImport([]string{
			"-db", importFixture(t), "-apply", "-master", srv.URL, "-token", "token",
		}, &out, &errBuf)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if !strings.Contains(out.String(), "0 inbound(s) (1 existed), 0 client record(s) (1 existed)") {
			t.Fatalf("run %d output:\n%s", run, out.String())
		}
	}
	if requests != 12 {
		t.Fatalf("expected 12 requests (2 runs x (1 spec probe + 3 entities + primary + alias)), got %d", requests)
	}
}

func TestImportApplySendsGroupMembers(t *testing.T) {
	var groupBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/inbounds/") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/sub/alice" {
			_, _ = w.Write([]byte(`{"token":"primary"}`))
			return
		}
		if r.URL.Path == "/groups" {
			_ = json.NewDecoder(r.Body).Decode(&groupBody)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	var out, errBuf strings.Builder
	if err := runImport([]string{
		"-db", importFixture(t), "-apply", "-master", srv.URL, "-token", "token",
	}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if groupBody["name"] != "friends" {
		t.Fatalf("group body %v", groupBody)
	}
	emails, _ := groupBody["emails"].([]any)
	if len(emails) != 1 || emails[0] != "alice" {
		t.Fatalf("group members %v", groupBody["emails"])
	}
}

// A node enrolled but not registered has no spec; apply creates an empty one
// first, without touching a spec that already exists.
func TestImportApplyRegistersAMissingNodeSpec(t *testing.T) {
	var registered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/inbounds/ru4":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/nodes/ru4/spec":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["node"] != "ru4" {
				t.Errorf("spec body %v", body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			registered = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/sub/alice":
			_, _ = w.Write([]byte(`{"token":"primary"}`))
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	var out, errBuf strings.Builder
	if err := runImport([]string{
		"-db", importFixture(t), "-apply", "-master", srv.URL, "-token", "token",
	}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("the empty spec was not registered")
	}
	if !strings.Contains(errBuf.String(), "registered an empty spec for ru4") {
		t.Fatalf("stderr %q", errBuf.String())
	}
}

func TestImportVerifyComparesCountsAndLinks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inbounds/ru4":
			_, _ = w.Write([]byte(`[{"tag":"in-443"}]`))
		case "/clients":
			_, _ = w.Write([]byte(`[{"node":"ru4","inbound":"in-443","email":"alice","uuid":"` +
				importFixtureUUID + `","enable":true,"totalBytes":0,"expiresAt":0,"limitIp":1}]`))
		case "/groups":
			_, _ = w.Write([]byte(`[{"name":"friends","emails":["alice"]}]`))
		case "/sub":
			_, _ = w.Write([]byte(`[{"token":"primary","email":"alice","links":1,"aliases":["s1"]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	run := func(srv *httptest.Server) (string, error) {
		var out, errBuf strings.Builder
		err := runImport([]string{"-db", importFixture(t), "-verify", "-master", srv.URL, "-token", "token"}, &out, &errBuf)
		return out.String(), err
	}
	srv := httptest.NewServer(handler)
	if out, err := run(srv); err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	} else {
		var res verifyResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("verify output: %v\n%s", err, out)
		}
		if !res.OK() || res.Inbounds.Found != 1 || res.Clients.Found != 1 || res.Groups.Found != 1 || res.Subscriptions.Found != 1 || res.Aliases.Found != 1 {
			t.Fatalf("result %+v", res)
		}
	}
	srv.Close()

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inbounds/ru4", "/clients":
			_, _ = w.Write([]byte(`[]`))
		case "/groups":
			_, _ = w.Write([]byte(`[]`))
		case "/sub":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer missing.Close()
	out, err := run(missing)
	if err == nil {
		t.Fatalf("verify passed with nothing on the master:\n%s", out)
	}
	if !strings.Contains(out, "is missing") {
		t.Fatalf("verify did not report the gaps:\n%s", out)
	}
}
