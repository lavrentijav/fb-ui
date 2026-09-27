package service

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
)

func TestDomainRewriterRenamesWholeNamesOnly(t *testing.T) {
	rw := newDomainRewriter("walk.old.com", "walk.new.net")
	in := `{"serverName":"ru2.walk.old.com","host":"RU.Walk.Old.Com","apex":"walk.old.com",` +
		`"certificateFile":"/root/cert/ru2.walk.old.com/fullchain.pem","other":"fire-walk.old.com",` +
		`"longer":"walk.old.com.evil.net","end":"a.walk.old.com."}`
	want := `{"serverName":"ru2.walk.new.net","host":"ru.walk.new.net","apex":"walk.new.net",` +
		`"certificateFile":"/root/cert/ru2.walk.old.com/fullchain.pem","other":"fire-walk.old.com",` +
		`"longer":"walk.old.com.evil.net","end":"a.walk.new.net."}`
	if got := rw.rewrite(in); got != want {
		t.Fatalf("rewrite:\n got  %s\n want %s", got, want)
	}
	if got := rw.names(in); !slices.Equal(got, []string{"ru2.walk.old.com", "ru.walk.old.com", "walk.old.com", "a.walk.old.com"}) {
		t.Fatalf("names = %v", got)
	}
}

// Moving the fleet from walk.old.com to walk.new.net: records copied with
// the same addresses, certificates extended, and in switch mode the nodes,
// host groups and inbounds pointed at the new names.
func TestDomainMigrateSwitch(t *testing.T) {
	initCertTestDB(t)
	ca := installFakeCA(t)
	rec := &recordingDNS{}
	oldFactory := dnsProviderFactory
	dnsProviderFactory = func(dnsprovider.Config) (dnsprovider.Provider, error) { return rec, nil }
	oldWait, oldLookup := dnsWaitVisible, migrateLookupIP
	dnsWaitVisible = func(context.Context, string, string, string, []string, time.Duration) error { return nil }
	migrateLookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		switch host {
		case "ru7.walk.old.com":
			return []net.IP{net.ParseIP("203.0.113.7")}, nil
		case "ru.walk.old.com":
			return []net.IP{net.ParseIP("203.0.113.8"), net.ParseIP("203.0.113.7")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	t.Cleanup(func() { dnsProviderFactory, dnsWaitVisible, migrateLookupIP = oldFactory, oldWait, oldLookup })
	addZone(t, "old.com")
	addZone(t, "new.net")

	db := database.GetDB()
	node := &model.Node{Name: "ru7", Address: "ru7.walk.old.com", Port: 1367, Role: model.NodeRoleNode, Enable: true, ApiToken: "x"}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	host := &model.Host{GroupId: "g1", InboundId: 1, Remark: "DE", Address: "ru.walk.old.com", Sni: "ru.walk.old.com", HostHeader: "ru.walk.old.com", Port: 443}
	if err := db.Create(host).Error; err != nil {
		t.Fatal(err)
	}
	route := &model.EdgeRoute{Name: "pool ws", Kind: "http", Enable: true, Match: "ru.walk.old.com", PathPrefix: "/ws-de", Action: "proxy", Target: "http://127.0.0.1:12053"}
	if err := db.Create(route).Error; err != nil {
		t.Fatal(err)
	}
	var certs CertificateService
	cert, err := certs.Create(&CertificateRequest{Domains: []string{"ru7.walk.old.com", "ru.walk.old.com"}})
	if err != nil {
		t.Fatal(err)
	}

	var s DomainMigrateService
	req := &DomainMigrateRequest{From: "walk.old.com", To: "walk.new.net", Mode: DomainMigrateSwitch}
	plan, err := s.Plan(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.sets) != 0 || len(ca.requests) != 0 {
		t.Fatalf("plan changed something: dns=%v issues=%d", rec.sets, len(ca.requests))
	}
	kinds := map[string]int{}
	for _, a := range plan {
		kinds[a.Kind]++
	}
	if kinds["dns"] != 2 || kinds["cert"] != 1 || kinds["host"] != 1 || kinds["node"] != 1 || kinds["edge"] != 1 {
		t.Fatalf("plan = %+v", plan)
	}

	done, err := s.Apply(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range done {
		if !a.Done {
			t.Fatalf("action failed: %+v", a)
		}
	}
	slices.Sort(rec.sets)
	if want := []string{"ru.walk.new.net A 203.0.113.7,203.0.113.8", "ru7.walk.new.net A 203.0.113.7"}; !slices.Equal(rec.sets, want) {
		t.Fatalf("records = %v", rec.sets)
	}
	got, _ := certs.GetById(cert.Id)
	if !slices.Equal(got.Domains, []string{"ru7.walk.old.com", "ru.walk.old.com", "ru7.walk.new.net", "ru.walk.new.net"}) || len(ca.requests) != 1 {
		t.Fatalf("certificate domains = %v, issued %d times", got.Domains, len(ca.requests))
	}
	var n model.Node
	var h model.Host
	db.First(&n, node.Id)
	db.First(&h, host.Id)
	var routes []model.EdgeRoute
	db.Order("id asc").Find(&routes)
	if len(routes) != 2 || routes[1].Match != "ru.walk.new.net" || routes[1].PathPrefix != "/ws-de" || routes[1].Target != route.Target {
		t.Fatalf("edge routes = %+v", routes)
	}
	if again, _ := s.Plan(req); slices.ContainsFunc(again, func(a DomainMigrateAction) bool { return a.Kind == "edge" || a.Kind == "cert" }) {
		t.Fatalf("a second run plans the same copies again: %+v", again)
	}
	if n.Address != "ru7.walk.new.net" || h.Address != "ru.walk.new.net" || h.Sni != "ru.walk.new.net" || h.HostHeader != "ru.walk.new.net" {
		t.Fatalf("node=%q host=%+v", n.Address, h)
	}

	for _, bad := range []DomainMigrateRequest{{From: "walk.old.com", To: "x.walk.old.com"}, {From: "walk.old.com", To: "walk.nobody.org"}, {From: "a", To: "b.c"}} {
		if _, err := s.Plan(&bad); err == nil {
			t.Fatalf("bad request %+v accepted", bad)
		}
	}
}
