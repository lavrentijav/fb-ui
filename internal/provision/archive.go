package provision

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReleaseSource describes the running panel so it can ship itself to a node
// in the layout of a GitHub release tarball (x-ui/x-ui, x-ui/bin/...).
type ReleaseSource struct {
	Binary string
	BinDir string
	// Fallback holds x-ui.sh and the x-ui.service.* units for installs that
	// lack them next to the binary (a hand-run lab copy, a dev build).
	Fallback map[string][]byte
}

var releaseScripts = []string{"x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel"}

// shippedBinFile keeps the master's own state out of the archive: config.json
// and mtproto/ hold this panel's inbounds and secrets, not release assets.
func shippedBinFile(name string) bool {
	switch {
	case strings.HasPrefix(name, "xray-linux-"), strings.HasPrefix(name, "mtg-linux-"):
		return true
	case strings.HasSuffix(name, ".dat"):
		return true
	case name == "LICENSE" || name == "README.md", name == "tuic-server":
		return true
	}
	return false
}

// WriteReleaseArchive streams a gzipped release tarball of src to w.
func WriteReleaseArchive(w io.Writer, src ReleaseSource) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, "x-ui/x-ui", src.Binary, 0o755); err != nil {
		return err
	}
	dir := filepath.Dir(src.Binary)
	for _, name := range releaseScripts {
		mode := int64(0o644)
		if name == "x-ui.sh" {
			mode = 0o755
		}
		onDisk := filepath.Join(dir, name)
		if _, err := os.Stat(onDisk); err == nil {
			if err := addFile(tw, "x-ui/"+name, onDisk, mode); err != nil {
				return err
			}
			continue
		}
		data, ok := src.Fallback[name]
		if !ok {
			if name == "x-ui.sh" {
				return fmt.Errorf("provision: no x-ui.sh next to %s and none embedded", src.Binary)
			}
			continue
		}
		if err := addBytes(tw, "x-ui/"+name, data, mode); err != nil {
			return err
		}
	}

	entries, err := os.ReadDir(src.BinDir)
	if err != nil {
		return fmt.Errorf("provision: read bin dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && shippedBinFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	hasXray := false
	for _, name := range names {
		hasXray = hasXray || strings.HasPrefix(name, "xray-linux-")
		mode := int64(0o644)
		if !strings.HasSuffix(name, ".dat") && name != "LICENSE" && name != "README.md" {
			mode = 0o755
		}
		if err := addFile(tw, "x-ui/bin/"+name, filepath.Join(src.BinDir, name), mode); err != nil {
			return err
		}
	}
	if !hasXray {
		return fmt.Errorf("provision: no xray-linux-* binary in %s", src.BinDir)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addFile(tw *tar.Writer, name, path string, mode int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addBytes(tw *tar.Writer, name string, data []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
