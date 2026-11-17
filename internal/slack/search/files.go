package search

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
)

// filesPageSize is the files.list page size.
const filesPageSize = 100

// fileHit is a file that matched, before download.
type fileHit struct {
	id        string
	title     string
	name      string
	mimetype  string
	filetype  string
	mode      string
	url       string
	permalink string
	// created is when the file was uploaded, in Unix seconds (0 if
	// unknown).
	created int64
}

// parseFileHit reads a raw Slack file object. ok is false when it has no
// ID.
func parseFileHit(f map[string]any) (hit fileHit, ok bool) {
	created, ok := jsonx.Num(f["timestamp"])
	if !ok {
		created, _ = jsonx.Num(f["created"])
	}
	hit = fileHit{
		id:        jsonx.Str(f["id"]),
		title:     strings.TrimSpace(jsonx.Str(f["title"])),
		name:      strings.TrimSpace(jsonx.Str(f["name"])),
		mimetype:  jsonx.Str(f["mimetype"]),
		filetype:  jsonx.Str(f["filetype"]),
		mode:      jsonx.Str(f["mode"]),
		url:       cmp.Or(jsonx.Str(f["url_private_download"]), jsonx.Str(f["url_private"])),
		permalink: jsonx.Str(f["permalink"]),
		created:   int64(created),
	}
	return hit, hit.id != ""
}

func (f fileHit) ts() string {
	if f.created <= 0 {
		return ""
	}
	return strconv.FormatInt(f.created, 10)
}

// searchFiles finds files with search.files, requesting pages until limit
// files pass the content-type filter.
func (s *searcher) searchFiles(ctx context.Context) ([]fileHit, *More, error) {
	p := &pager{client: s.client, method: "search.files", key: "files", query: s.query, size: min(s.limit, searchPageSize)}
	if s.contentType != ContentAny && s.contentType != ContentFile {
		// Some matches will be filtered out: ask for full pages.
		p.size = searchPageSize
	}

	var (
		hits   []fileHit
		seen   = map[string]bool{}
		left   int // matches of the last page not looked at
		reason string
	)
	for len(hits) < s.limit && !p.done {
		matches, err := p.next(ctx)
		if err != nil {
			if len(hits) > 0 && isRateLimit(err) {
				reason = fmt.Sprintf("The search stopped early: %v", err)
				break
			}
			return nil, nil, err
		}
		for i, m := range matches {
			f, ok := parseFileHit(m)
			if !ok || seen[f.id] {
				continue
			}
			seen[f.id] = true
			if !passesFile(f, s.contentType) {
				continue
			}
			hits = append(hits, f)
			if len(hits) >= s.limit {
				left = len(matches) - i - 1
				break
			}
		}
	}
	if reason == "" && left == 0 && p.done {
		return hits, nil, nil
	}
	return hits, s.moreFiles(hits, p.total, reason), nil
}

func (s *searcher) moreFiles(hits []fileHit, total int, reason string) *More {
	newest, oldest := "", ""
	if len(hits) > 0 {
		newest, oldest = hits[0].ts(), hits[len(hits)-1].ts()
	}
	return s.more("files", total, newest, oldest, reason)
}

// scanFiles finds files by listing those shared in each channel and
// matching their title and name locally. Channels are listed concurrently
// and their matches merged, newest first.
func (s *searcher) scanFiles(ctx context.Context) ([]fileHit, *More, error) {
	found := make([][]fileHit, len(s.channelIDs))
	cut := make([]bool, len(s.channelIDs))
	err := forEach(ctx, len(s.channelIDs), scanConcurrency, func(ctx context.Context, i int) error {
		var err error
		found[i], cut[i], err = s.scanChannelFiles(ctx, s.channelIDs[i])
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	all := slices.Concat(found...)
	slices.SortStableFunc(all, func(a, b fileHit) int { return cmp.Compare(b.created, a.created) })
	// A file shared in several of the channels is one result.
	seen := map[string]bool{}
	all = slices.DeleteFunc(all, func(f fileHit) bool {
		dup := seen[f.id]
		seen[f.id] = true
		return dup
	})
	incomplete := slices.Contains(cut, true)
	if len(all) > s.limit {
		all, incomplete = all[:s.limit], true
	}
	if !incomplete {
		return all, nil, nil
	}
	return all, s.moreFiles(all, 0, ""), nil
}

// scanChannelFiles returns the first limit files of a channel that match,
// and whether the listing was left before its end.
func (s *searcher) scanChannelFiles(ctx context.Context, channelID string) (out []fileHit, cut bool, err error) {
	base := map[string]any{"channel": channelID, "count": filesPageSize}
	if s.userID != "" {
		base["user"] = s.userID
	}
	// files.list bounds are inclusive.
	if !s.days.oldest.IsZero() {
		base["ts_from"] = s.days.oldest.Unix()
	}
	if !s.days.latest.IsZero() {
		base["ts_to"] = s.days.latest.Unix() - 1
	}

	seen := map[string]bool{}
	for page := 1; ; page++ {
		params := map[string]any{"page": page}
		for k, v := range base {
			params[k] = v
		}
		resp, err := s.client.API(ctx, "files.list", params)
		if err != nil {
			if page > 1 && isRateLimit(err) {
				s.warnf("the listing of the files of channel %s stopped early: %v", channelID, err)
				return out, true, nil
			}
			return nil, false, fmt.Errorf("listing the files of channel %s: %w", channelID, err)
		}
		raw := jsonx.Maps(resp["files"])
		pages, ok := jsonx.Int(jsonx.Get(resp, "paging", "pages"))
		if !ok {
			pages, _ = jsonx.Int(jsonx.Get(resp, "pagination", "page_count"))
		}
		// Without paging information, a short page is the last one.
		lastPage := len(raw) < filesPageSize
		if pages > 0 {
			lastPage = page >= pages
		}

		fresh := false
		for i, m := range raw {
			f, ok := parseFileHit(m)
			if !ok || seen[f.id] {
				continue
			}
			seen[f.id], fresh = true, true
			if !passesFile(f, s.contentType) || !s.match.matches(f.title+" "+f.name) {
				continue
			}
			out = append(out, f)
			if len(out) >= s.limit {
				return out, !lastPage || i < len(raw)-1, nil
			}
		}
		// A page without anything new means the listing is over, however
		// many pages Slack announced (or failed to announce).
		if lastPage || !fresh {
			return out, false, nil
		}
	}
}

// downloadFiles downloads the matched files and converts them to the
// shape printed. A file that cannot be downloaded is kept, with the
// reason.
func (s *searcher) downloadFiles(ctx context.Context, hits []fileHit) ([]File, error) {
	items := make([]download, len(hits))
	for i, f := range hits {
		items[i] = download{id: f.id, url: f.url, name: downloadName(f.id, f.mimetype, f.filetype, f.name, f.title)}
	}
	results, err := s.download(ctx, items)
	if err != nil {
		return nil, err
	}
	out := make([]File, len(hits))
	for i, f := range hits {
		res := results[f.id]
		out[i] = File{
			Title:     cmp.Or(f.title, f.name),
			Mimetype:  f.mimetype,
			Mode:      f.mode,
			Path:      res.Path,
			Error:     res.Error,
			Permalink: f.permalink,
		}
	}
	return out, nil
}
