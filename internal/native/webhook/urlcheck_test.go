package webhook

import (
	"context"
	"net"
	"testing"
)

func TestCheckPublicURLRefusesPrivateTargets(t *testing.T) {
	old := LookupIP
	t.Cleanup(func() { LookupIP = old })
	LookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "internal.example":
			return []net.IP{net.ParseIP("192.168.1.1")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
		case "ok.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	for _, bad := range []string{
		"http://127.0.0.1:9/x", "http://10.0.0.1/x", "http://169.254.169.254/latest/meta-data",
		"http://localhost/x", "http://[::1]/x", "http://100.64.0.1/x", "http://0.0.0.0/x",
		"http://internal.example/x", "http://mixed.example/x", "http://nowhere.example/x",
		"ftp://ok.example/x", "not a url", "", "http:///x",
	} {
		if err := CheckPublicURL(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	for _, good := range []string{"https://ok.example/hook", "http://93.184.216.34/h"} {
		if err := CheckPublicURL(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}
