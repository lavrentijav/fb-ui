package hosts_test

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hosts"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func TestSaveListDelete(t *testing.T) {
	h := modtest.NewHost("m1")
	ctx := context.Background()
	e, err := hosts.Save(ctx, h, hosts.Entry{
		Node: "ru4", Inbound: "vpn", Address: "cdn.example", Port: 8443,
		Security: "tls", SNI: "ru.lab.test", Remark: "cdn", Enable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" {
		t.Fatal("no id")
	}
	second, err := hosts.Save(ctx, h, hosts.Entry{Node: "ru4", Inbound: "vpn", Address: "1.2.3.4", Enable: true})
	if err != nil || second.ID == e.ID {
		t.Fatalf("second %+v %v", second, err)
	}
	list := hosts.List(h)
	if len(list) != 2 || list[0].Address != "cdn.example" || list[1].Address != "1.2.3.4" {
		t.Fatalf("list %+v", list)
	}
	if err := hosts.Delete(ctx, h, e); err != nil {
		t.Fatal(err)
	}
	if list := hosts.List(h); len(list) != 1 {
		t.Fatalf("after delete %+v", list)
	}
}

func TestValidate(t *testing.T) {
	base := hosts.Entry{Node: "ru4", Inbound: "vpn", Address: "cdn.example"}
	for name, change := range map[string]func(*hosts.Entry){
		"no node":        func(e *hosts.Entry) { e.Node = "" },
		"no inbound":     func(e *hosts.Entry) { e.Inbound = "" },
		"empty address":  func(e *hosts.Entry) { e.Address = "" },
		"scheme in addr": func(e *hosts.Entry) { e.Address = "https://cdn.example" },
		"bad chars":      func(e *hosts.Entry) { e.Address = "a b" },
		"bad port":       func(e *hosts.Entry) { e.Port = 70000 },
		"bad security":   func(e *hosts.Entry) { e.Security = "reality" },
	} {
		e := base
		change(&e)
		if err := e.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	for _, good := range []hosts.Entry{base, {Node: "a", Inbound: "b", Address: "1.2.3.4", Port: 443}, {Node: "a", Inbound: "b", Address: "x.y", Security: "none"}} {
		if err := good.Validate(); err != nil {
			t.Fatalf("%+v: %v", good, err)
		}
	}
}

func TestEndpointsSkipDisabled(t *testing.T) {
	got := hosts.Endpoints([]hosts.Entry{
		{Node: "ru4", Inbound: "vpn", Address: "a.example", Enable: true},
		{Node: "ru4", Inbound: "vpn", Address: "b.example", Enable: false},
	})
	if len(got["ru4/vpn"]) != 1 || got["ru4/vpn"][0].Address != "a.example" {
		t.Fatalf("%+v", got)
	}
}
