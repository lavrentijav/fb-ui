package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestParseRemoteListReadsPublishedShapes(t *testing.T) {
	body := []byte("# antifilter style\nAds.Example.com.\n0.0.0.0 tracker.example.net\n127.0.0.1 localhost\n" +
		"||adblock.example.org^$third-party\n! adblock comment\nfull:exact.example.com\nbad entry with spaces\n" +
		"ads.example.com\n")
	got, skipped := parseRemoteList(model.FilterKindDomain, body)
	want := []string{"ads.example.com", "tracker.example.net", "adblock.example.org", "full:exact.example.com"}
	if !slices.Equal(got, want) || skipped != 1 {
		t.Fatalf("domains = %v skipped %d, want %v skipped 1", got, skipped, want)
	}
	ips, skipped := parseRemoteList(model.FilterKindIP, []byte("10.0.0.0/8\n# c\n203.0.113.7\nnot-an-ip\n2001:db8::/32\n"))
	if !slices.Equal(ips, []string{"10.0.0.0/8", "203.0.113.7", "2001:db8::/32"}) || skipped != 1 {
		t.Fatalf("ips = %v skipped %d", ips, skipped)
	}
}

func TestRemoteListFeedsTheRuleAndSurvivesAFailedFetch(t *testing.T) {
	initFilterTestDB(t)
	var body []byte
	var fetchErr error
	old := remoteListFetcher
	remoteListFetcher = func(context.Context, string) ([]byte, error) { return body, fetchErr }
	t.Cleanup(func() { remoteListFetcher = old })
	var s FilterService

	list := &model.FilterList{
		Name: "blocked-in-ru", Kind: model.FilterKindDomain, Entries: []string{"mine.example.com"},
		Enable: true, SourceUrl: "https://lists.example.org/ru.lst",
	}
	if err := s.CreateList(list); err != nil {
		t.Fatal(err)
	}
	body = []byte("a.example.com\nb.example.com\n")
	if _, err := s.RefreshList(list.Id); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListById(list.Id)
	if got.RemoteCount != 2 || got.FetchedAt == 0 || got.FetchError != "" {
		t.Fatalf("after fetch: %+v", got)
	}
	rule := &model.FilterRule{Id: 1, PanelId: SelfPanelId, ListIds: []int{list.Id}, Action: model.FilterActionBlock, Enable: true}
	domains, _ := ruleMatchers(rule, map[int]*model.FilterList{list.Id: got})
	if !slices.Equal(domains, []string{"mine.example.com", "a.example.com", "b.example.com"}) {
		t.Fatalf("rule matchers = %v, want own entries then fetched ones", domains)
	}

	fetchErr = errors.New("connection refused")
	if _, err := s.RefreshList(list.Id); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("failed fetch: %v", err)
	}
	got, _ = s.ListById(list.Id)
	if got.RemoteCount != 2 || len(got.RemoteEntries) != 2 || !strings.Contains(got.FetchError, "connection refused") {
		t.Fatalf("a failed fetch must keep the last good entries: %+v", got)
	}

	fetchErr = nil
	got.SourceUrl = "https://lists.example.org/other.lst"
	if err := s.UpdateList(list.Id, got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListById(list.Id)
	if len(got.RemoteEntries) != 0 || got.FetchedAt != 0 {
		t.Fatalf("entries of the old source must go with it: %+v", got)
	}
}
