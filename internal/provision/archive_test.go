package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteReleaseArchiveShipsAssetsNotState(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(filepath.Join(bin, "mtproto"), 0o755)
	write := func(p, s string) { _ = os.WriteFile(filepath.Join(root, p), []byte(s), 0o644) }
	write("x-ui", "ELF-panel")
	write("x-ui.service.debian", "[Unit] from disk")
	write("bin/xray-linux-amd64", "ELF-xray")
	write("bin/geosite_RU.dat", "geo")
	write("bin/config.json", `{"inbounds":"master secrets"}`)
	write("bin/mtproto/1.toml", "secret")
	write("bin/tuic-server", "ELF-tuic")

	var buf bytes.Buffer
	err := WriteReleaseArchive(&buf, ReleaseSource{
		Binary:   filepath.Join(root, "x-ui"),
		BinDir:   bin,
		Fallback: map[string][]byte{"x-ui.sh": []byte("#!/bin/bash embedded"), "x-ui.service.debian": []byte("embedded unit")},
	})
	if err != nil {
		t.Fatalf("WriteReleaseArchive: %v", err)
	}

	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	modes := map[string]int64{}
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name], modes[h.Name] = string(b), h.Mode
		names = append(names, h.Name)
	}
	want := []string{"x-ui/x-ui", "x-ui/x-ui.sh", "x-ui/x-ui.service.debian", "x-ui/bin/geosite_RU.dat", "x-ui/bin/tuic-server", "x-ui/bin/xray-linux-amd64"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	if got["x-ui/x-ui.service.debian"] != "[Unit] from disk" {
		t.Fatalf("a unit next to the binary must win over the embedded one: %q", got["x-ui/x-ui.service.debian"])
	}
	if !strings.Contains(got["x-ui/x-ui.sh"], "embedded") || modes["x-ui/x-ui.sh"] != 0o755 || modes["x-ui/bin/xray-linux-amd64"] != 0o755 || modes["x-ui/bin/tuic-server"] != 0o755 {
		t.Fatalf("x-ui.sh=%q modes=%v", got["x-ui/x-ui.sh"], modes)
	}

	_ = os.Remove(filepath.Join(bin, "xray-linux-amd64"))
	err = WriteReleaseArchive(io.Discard, ReleaseSource{Binary: filepath.Join(root, "x-ui"), BinDir: bin, Fallback: map[string][]byte{"x-ui.sh": nil}})
	if err == nil || !strings.Contains(err.Error(), "no xray-linux-* binary") {
		t.Fatalf("missing xray: err = %v", err)
	}
}
