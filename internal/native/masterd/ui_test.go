package masterd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func uiDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>app"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets/app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Daemon{cfg: Config{UIDir: dir}}
}

func TestUIServesFilesAndClientRoutes(t *testing.T) {
	d := uiDaemon(t)
	for path, want := range map[string]string{
		"/panel/":              "<!doctype html>app",
		"/panel":               "<!doctype html>app",
		"/panel/nodes":         "<!doctype html>app", // a client-side route
		"/panel/assets/app.js": "console.log(1)",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/panel" {
			http.Redirect(rec, req, "/panel/", http.StatusFound)
			if rec.Code != http.StatusFound {
				t.Fatalf("%s: %d", path, rec.Code)
			}
			continue
		}
		d.serveUI(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestUINoBuild(t *testing.T) {
	d := &Daemon{cfg: Config{UIDir: t.TempDir()}}
	rec := httptest.NewRecorder()
	d.serveUI(rec, httptest.NewRequest(http.MethodGet, "/panel/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an empty dir: %d", rec.Code)
	}
}
