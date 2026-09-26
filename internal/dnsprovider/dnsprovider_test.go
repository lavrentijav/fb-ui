package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeCloudflare is a stateful stand-in for the v4 API subset we call. It
// records the verb sequence so tests can assert add-before-delete.
type fakeCloudflare struct {
	mu      sync.Mutex
	nextID  int
	records map[string]cfRecord
	calls   []string
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(result any) {
		b, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(cfEnvelope{Success: true, Result: b})
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(cfEnvelope{Errors: []cfError{{Code: 10000, Message: "Authentication error"}}})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		reply([]map[string]string{{"id": "z1", "name": r.URL.Query().Get("name")}})
	case r.Method == http.MethodGet && r.URL.Path == "/zones/z1/dns_records":
		var out []cfRecord
		for _, rec := range f.records {
			if rec.Name == r.URL.Query().Get("name") && rec.Type == r.URL.Query().Get("type") {
				out = append(out, rec)
			}
		}
		reply(out)
	case r.Method == http.MethodPost && r.URL.Path == "/zones/z1/dns_records":
		var rec cfRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		f.nextID++
		rec.ID = fmt.Sprintf("r%d", f.nextID)
		f.records[rec.ID] = rec
		f.calls = append(f.calls, "add "+rec.Content)
		reply(rec)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(r.URL.Path, "/zones/z1/dns_records/")
		f.calls = append(f.calls, "del "+f.records[id].Content)
		delete(f.records, id)
		reply(map[string]string{"id": id})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCloudflare) contents(name, rtype string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, rec := range f.records {
		if rec.Name == name && rec.Type == rtype {
			out = append(out, rec.Content)
		}
	}
	slices.Sort(out)
	return out
}

func TestCloudflareSetRecordsConverges(t *testing.T) {
	fake := &fakeCloudflare{records: map[string]cfRecord{
		"old1": {ID: "old1", Type: "A", Name: "ru.fire-core.ru", Content: "10.0.0.1"},
		"old2": {ID: "old2", Type: "A", Name: "ru.fire-core.ru", Content: "10.0.0.2"},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	p, err := New(Config{Type: TypeCloudflare, Zone: "Fire-Core.ru.", Credential: "tok", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.SetRecords(ctx, "RU.fire-core.ru", "a", []string{"10.0.0.2", "10.0.0.3"}, 60); err != nil {
		t.Fatalf("SetRecords: %v", err)
	}
	if got, want := fake.contents("ru.fire-core.ru", "A"), []string{"10.0.0.2", "10.0.0.3"}; !slices.Equal(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
	if want := []string{"add 10.0.0.3", "del 10.0.0.1"}; !slices.Equal(fake.calls, want) {
		t.Fatalf("calls = %v, want %v (kept record must not be touched, add must precede delete)", fake.calls, want)
	}

	if err := p.SetRecords(ctx, "_acme-challenge.ru.fire-core.ru", "TXT", []string{"tokA"}, 0); err != nil {
		t.Fatalf("SetRecords TXT: %v", err)
	}
	if got := fake.contents("_acme-challenge.ru.fire-core.ru", "TXT"); !slices.Equal(got, []string{`"tokA"`}) {
		t.Fatalf("TXT = %v, want quoted tokA", got)
	}
	fake.calls = nil
	if err := p.SetRecords(ctx, "_acme-challenge.ru.fire-core.ru", "TXT", []string{"tokA"}, 0); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("re-applying a quoted TXT value rewrote it: %v", fake.calls)
	}
	if err := p.DeleteRecords(ctx, "_acme-challenge.ru.fire-core.ru", "TXT"); err != nil {
		t.Fatal(err)
	}
	if got := fake.contents("_acme-challenge.ru.fire-core.ru", "TXT"); len(got) != 0 {
		t.Fatalf("TXT after delete = %v", got)
	}

	if err := p.SetRecords(ctx, "evil.example.com", "A", []string{"1.1.1.1"}, 0); err == nil || !strings.Contains(err.Error(), "outside zone") {
		t.Fatalf("out-of-zone write: err = %v, want outside zone", err)
	}
}

func TestCloudflareBadTokenSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(&fakeCloudflare{records: map[string]cfRecord{}})
	t.Cleanup(srv.Close)
	p, _ := New(Config{Type: TypeCloudflare, Zone: "fire-core.ru", Credential: "wrong", BaseURL: srv.URL})
	err := p.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "10000 Authentication error") {
		t.Fatalf("Check err = %v, want the API's auth error", err)
	}
}

// fakeTechnitium models the records/{get,add,delete} endpoints, which take
// form-encoded parameters and identify a record by its value.
type fakeTechnitium struct {
	mu      sync.Mutex
	records []tnRecord
	calls   []string
}

func (f *fakeTechnitium) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	if r.URL.RawQuery != "" {
		http.Error(w, "token must not travel in the URL", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("token") != "tok" {
		_ = json.NewEncoder(w).Encode(tnEnvelope{Status: "invalid-token", ErrorMessage: "Invalid token"})
		return
	}
	ok := func(resp any) {
		b, _ := json.Marshal(resp)
		_ = json.NewEncoder(w).Encode(tnEnvelope{Status: "ok", Response: b})
	}
	name, rtype := r.PostForm.Get("domain"), r.PostForm.Get("type")
	value := r.PostForm.Get("ipAddress") + r.PostForm.Get("text")
	switch r.URL.Path {
	case "/api/zones/list":
		ok(map[string]any{"zones": []map[string]any{
			{"name": "boykisser.online", "type": "Primary"},
			{"name": "Lab.Boykisser.Online", "type": "Primary"},
			{"name": "mirror.example", "type": "Secondary"},
			{"name": "localhost", "type": "Primary", "internal": true},
			{"name": "old.example", "type": "Primary", "disabled": true},
		}})
	case "/api/zones/records/get":
		var out []tnRecord
		for _, rec := range f.records {
			if rec.Name == name {
				out = append(out, rec)
			}
		}
		ok(map[string]any{"records": out})
	case "/api/zones/records/add":
		field := "ipAddress"
		if rtype == "TXT" {
			field = "text"
		}
		rd, _ := json.Marshal(map[string]string{field: value})
		f.records = append(f.records, tnRecord{Name: name, Type: rtype, RData: rd})
		f.calls = append(f.calls, "add "+value)
		ok(nil)
	case "/api/zones/records/delete":
		f.records = slices.DeleteFunc(f.records, func(rec tnRecord) bool {
			return rec.Name == name && rec.Type == rtype && recordValue(rtype, rec.RData) == value
		})
		f.calls = append(f.calls, "del "+value)
		ok(nil)
	default:
		_ = json.NewEncoder(w).Encode(tnEnvelope{Status: "error", ErrorMessage: "no such call"})
	}
}

func TestTechnitiumSetRecordsConverges(t *testing.T) {
	a1, _ := json.Marshal(map[string]string{"ipAddress": "2001:db8::1"})
	ns, _ := json.Marshal(map[string]string{"nameServer": "ns1.boykisser.online"})
	fake := &fakeTechnitium{records: []tnRecord{
		{Name: "walk.boykisser.online", Type: "AAAA", RData: a1},
		{Name: "walk.boykisser.online", Type: "NS", RData: ns},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	if _, err := New(Config{Type: TypeTechnitium, Zone: "boykisser.online", Credential: "tok"}); err == nil {
		t.Fatal("technitium without a server URL must be rejected")
	}
	p, err := New(Config{Type: TypeTechnitium, Zone: "boykisser.online", Credential: "tok", BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 2001:0db8::0001 is the stored address spelled differently: it must be kept.
	if err := p.SetRecords(ctx, "walk.boykisser.online", "AAAA", []string{"2001:0db8::0001", "2001:db8::2"}, 120); err != nil {
		t.Fatalf("SetRecords: %v", err)
	}
	if want := []string{"add 2001:db8::2"}; !slices.Equal(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if err := p.DeleteRecords(ctx, "walk.boykisser.online", "AAAA"); err != nil {
		t.Fatal(err)
	}
	if len(fake.records) != 1 || fake.records[0].Type != "NS" {
		t.Fatalf("DeleteRecords AAAA must leave only the NS record, got %+v", fake.records)
	}

	bad, _ := New(Config{Type: TypeTechnitium, Zone: "boykisser.online", Credential: "nope", BaseURL: srv.URL})
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "invalid or expired API token") {
		t.Fatalf("Check with bad token: err = %v", err)
	}
}

func TestTechnitiumListZonesKeepsWritablePrimaries(t *testing.T) {
	srv := httptest.NewServer(&fakeTechnitium{})
	t.Cleanup(srv.Close)
	zones, err := ListZones(context.Background(), Config{Type: TypeTechnitium, Credential: "tok", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"boykisser.online", "lab.boykisser.online"}; !slices.Equal(zones, want) {
		t.Fatalf("zones = %v, want %v", zones, want)
	}
	if got := ZoneFor("ru7.lab.boykisser.online", zones); got != "lab.boykisser.online" {
		t.Fatalf("ZoneFor sub-zone = %q", got)
	}
	if got := ZoneFor("fire-core.ru", zones); got != "" {
		t.Fatalf("ZoneFor foreign name = %q, want empty", got)
	}
}
