package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/edge"
)

func TestEdgeRefusesATableThatCannotWork(t *testing.T) {
	initCertTestDB(t)
	var applied []EdgeRuntime
	SetEdgeRuntime(func(rt EdgeRuntime) error { applied = append(applied, rt); return nil }, nil)
	t.Cleanup(func() { SetEdgeRuntime(nil, nil) })
	var s EdgeService

	bad := []struct {
		route model.EdgeRoute
		want  string
	}{
		{model.EdgeRoute{Kind: "http", Enable: true, PathPrefix: "/", Target: "http://127.0.0.1:8080"}, "would hide the panel"},
		{model.EdgeRoute{Kind: "sni", Enable: true, Match: "a.example.com", Action: "tcp", Target: "127.0.0.1"}, "is not host:port"},
		{model.EdgeRoute{Kind: "sni", Enable: true, Match: "a.*.example.com", Action: "tcp", Target: "127.0.0.1:1"}, "leading *. wildcard"},
		{model.EdgeRoute{Kind: "http", Enable: true, Match: "site.example.com", PathPrefix: "/", Target: "ftp://x"}, "must be an http(s) URL"},
		{model.EdgeRoute{Kind: "sni", Enable: true, Match: "x.example.com", Action: "proxy"}, "takes panel, tcp or reject"},
	}
	for _, tc := range bad {
		r := tc.route
		if _, err := s.Save(&r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Save(%+v) err = %v, want %q", tc.route, err, tc.want)
		}
	}
	if rows, _ := s.List(); len(rows) != 0 {
		t.Fatalf("refused routes were stored: %+v", rows)
	}

	ok := []model.EdgeRoute{
		{Name: "panel", Kind: "sni", Enable: true, Match: "Panel.Example.com.", Action: "panel"},
		{Name: "reality", Kind: "sni", Enable: true, Match: "*.microsoft.com", Action: "tcp", Target: "127.0.0.1:8443", ProxyProtocol: 2},
		{Name: "ws", Kind: "http", Enable: true, PathPrefix: "/ws", Target: "http://127.0.0.1:10001"},
		{Name: "off", Kind: "sni", Enable: false, Match: "off.example.com", Action: "tcp", Target: "bad"},
	}
	for i := range ok {
		if _, err := s.Save(&ok[i]); err != nil {
			t.Fatalf("Save(%s): %v", ok[i].Name, err)
		}
	}
	last := applied[len(applied)-1]
	if len(last.Config.Routes) != 2 || last.Config.Routes[0].Match != "panel.example.com" || last.Config.Routes[0].Action != edge.ActionHandoff ||
		last.Config.Routes[1].ProxyProtocol != 2 || len(last.HTTP) != 1 || last.Config.Default.Action != edge.ActionReject {
		t.Fatalf("applied table = %+v", last)
	}
}

func TestEdgeSettingsRollBackWhenThePortIsBusy(t *testing.T) {
	initCertTestDB(t)
	busy := errors.New("listen tcp :443: bind: address already in use")
	SetEdgeRuntime(func(rt EdgeRuntime) error {
		if rt.Settings.Enable {
			return busy
		}
		return nil
	}, nil)
	t.Cleanup(func() { SetEdgeRuntime(nil, nil) })
	var s EdgeService

	err := s.SetSettings(&EdgeSettings{Enable: true, Listen: ":443", DefaultAction: "reject"})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("SetSettings err = %v", err)
	}
	got, _ := s.GetSettings()
	if got.Enable {
		t.Fatal("settings that could not be applied were kept")
	}
	if err := s.SetSettings(&EdgeSettings{Enable: true, Listen: "443", DefaultAction: "reject"}); err == nil {
		t.Fatal("a listen address without a colon must be refused")
	}
}
