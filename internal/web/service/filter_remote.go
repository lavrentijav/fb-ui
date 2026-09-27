package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	// Published block lists run to hundreds of thousands of lines; past this
	// the Xray config itself becomes the problem.
	maxRemoteFilterEntries = 300000
	maxRemoteFilterBytes   = 32 << 20
	remoteFilterTimeout    = 60 * time.Second
)

// remoteListFetcher downloads a list body; tests replace it.
var remoteListFetcher = func(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fb-ui filter list")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteFilterBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRemoteFilterBytes {
		return nil, fmt.Errorf("list is larger than %d MiB", maxRemoteFilterBytes>>20)
	}
	return body, nil
}

// parseRemoteList reads the common published shapes: one entry per line,
// hosts files ("0.0.0.0 ads.example.com") and adblock ("||ads.example.com^").
// Comments are skipped; a line Xray would reject is counted, not fatal.
func parseRemoteList(kind string, body []byte) (entries []string, skipped int) {
	seen := map[string]struct{}{}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "[") {
			continue
		}
		if kind == model.FilterKindDomain {
			if f := strings.Fields(line); len(f) >= 2 && (f[0] == "0.0.0.0" || f[0] == "127.0.0.1" || f[0] == "::" || f[0] == "::1") {
				line = f[1]
			}
			if rest, ok := strings.CutPrefix(line, "||"); ok {
				line = strings.TrimSuffix(strings.SplitN(rest, "^", 2)[0], ".")
			}
			if !strings.Contains(line, ":") {
				line = strings.ToLower(strings.TrimSuffix(line, "."))
			}
			if line == "localhost" || line == "localhost.localdomain" {
				continue
			}
		}
		if validateFilterEntry(kind, line) != nil {
			skipped++
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		entries = append(entries, line)
	}
	return entries, skipped
}

// RefreshList fetches a list's source now. A failed fetch keeps the entries
// it had, so a source going down does not silently lift a block.
func (s *FilterService) RefreshList(id int) (*model.FilterList, error) {
	list, err := s.ListById(id)
	if err != nil {
		return nil, err
	}
	if list.SourceUrl == "" {
		return nil, common.NewError("this list has no source URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteFilterTimeout)
	defer cancel()
	now := time.Now().Unix()
	body, err := remoteListFetcher(ctx, list.SourceUrl)
	var entries []string
	skipped := 0
	if err == nil {
		entries, skipped = parseRemoteList(list.Kind, body)
		switch {
		case len(entries) == 0:
			err = fmt.Errorf("no usable entries (%d lines skipped)", skipped)
		case len(entries) > maxRemoteFilterEntries:
			err = fmt.Errorf("%d entries, the limit is %d", len(entries), maxRemoteFilterEntries)
		}
	}
	db := database.GetDB()
	if err != nil {
		if uerr := db.Model(&model.FilterList{}).Where("id = ?", id).
			Updates(map[string]any{"fetched_at": now, "fetch_error": err.Error()}).Error; uerr != nil {
			return nil, uerr
		}
		return nil, common.NewErrorf("fetch %s: %v", list.SourceUrl, err)
	}
	changed := !slices.Equal(entries, list.RemoteEntries)
	list.RemoteEntries, list.RemoteCount, list.FetchedAt, list.FetchError = entries, len(entries), now, ""
	if skipped > 0 {
		list.FetchError = fmt.Sprintf("%d lines skipped", skipped)
	}
	if err := db.Model(list).Select("RemoteEntries", "RemoteCount", "FetchedAt", "FetchError").Updates(list).Error; err != nil {
		return nil, err
	}
	if changed {
		s.listChanged(id)
	}
	return list, nil
}

// listChanged regenerates this panel's Xray when one of its own rules uses
// the list; a node's share changes version and is re-pushed by the sync job.
func (s *FilterService) listChanged(id int) {
	rules, err := s.Rules()
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Enable && r.PanelId == SelfPanelId && slices.Contains(r.ListIds, id) {
			(&XrayService{}).SetToNeedRestart()
			return
		}
	}
}

// RefreshDueLists is the job body: every list whose refresh interval passed.
func (s *FilterService) RefreshDueLists(now time.Time) {
	var lists []*model.FilterList
	if err := database.GetDB().Where("source_url <> '' AND enable = ?", true).Find(&lists).Error; err != nil {
		logger.Warning("filter lists: load:", err)
		return
	}
	for _, l := range lists {
		hours := l.RefreshHours
		if hours <= 0 {
			hours = 24
		}
		if now.Unix()-l.FetchedAt < int64(hours)*3600 {
			continue
		}
		if _, err := s.RefreshList(l.Id); err != nil {
			logger.Warning("filter list", l.Name, err)
		}
	}
}
