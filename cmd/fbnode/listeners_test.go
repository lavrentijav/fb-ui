package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The node must expose nothing but its public edge. The edge listener lives in
// edgerun (wired with config.Listen); the agent itself never accepts a
// management connection. This guards the rule at build time: a new listener in
// the node binary is a deliberate decision that must change this test.
func TestTheNodeOpensNoListenerOfItsOwn(t *testing.T) {
	forbidden := []string{
		"net.Listen(",
		"ListenAndServe(",
		"http.ListenAndServe",
		"grpc.NewServer(",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		checked++
		for _, bad := range forbidden {
			if strings.Contains(src, bad) {
				t.Fatalf("%s opens a listener (%q); the node may only serve the edge", name, bad)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no node sources were checked")
	}
}
