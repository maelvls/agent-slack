package search

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	// searchPageSize is the largest page search.* returns.
	searchPageSize = 100
	// searchMaxPages is the last page search.* serves.
	searchMaxPages = 100
)

// pager walks the pages of search.messages or search.files, newest
// matches first. Pages are fetched on demand, so that a search stops
// paying for pages as soon as it has enough results.
type pager struct {
	client *slackapi.Client
	// method is the API method, key the response field holding the
	// matches ("messages" or "files").
	method, key string
	query       string
	size        int

	page int
	// total is Slack's count of matches, known after the first page.
	total int
	// done is set once the last page has been served.
	done bool
}

// next returns the next page of matches. Call it only while !p.done.
func (p *pager) next(ctx context.Context) ([]map[string]any, error) {
	p.page++
	resp, err := p.client.API(ctx, p.method, map[string]any{
		"query":     p.query,
		"count":     p.size,
		"page":      p.page,
		"highlight": false,
		"sort":      "timestamp",
		"sort_dir":  "desc",
	})
	if err != nil {
		if slackapi.IsErrorCode(err, "not_allowed_token_type") {
			return nil, fmt.Errorf("%w (%s: not_allowed_token_type)", errSearchNotAllowed, p.method)
		}
		return nil, fmt.Errorf("searching %s for %q: %w", p.key, p.query, err)
	}
	section := jsonx.Map(resp[p.key])
	matches := jsonx.Maps(section["matches"])

	// Slack describes the pages in "paging" (pages, total) or, on newer
	// responses, in "pagination" (page_count, total_count).
	pages := 1
	for _, path := range [][2]string{{"paging", "pages"}, {"pagination", "page_count"}} {
		if n, ok := jsonx.Int(jsonx.Get(section, path[0], path[1])); ok && n > 0 {
			pages = n
			break
		}
	}
	for _, path := range [][2]string{{"paging", "total"}, {"pagination", "total_count"}} {
		if n, ok := jsonx.Int(jsonx.Get(section, path[0], path[1])); ok && n > 0 {
			p.total = n
			break
		}
	}
	p.done = len(matches) == 0 || p.page >= pages || p.page >= searchMaxPages
	return matches, nil
}

// tsTime converts a Slack timestamp ("1770165109.628379"), or a Unix time
// in seconds, to a time. ok is false when ts is not one.
func tsTime(ts string) (t time.Time, ok bool) {
	secs, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// unixTS formats t as a Slack timestamp.
func unixTS(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) + ".000000" }

// more describes an incomplete result list. noun is "messages" or
// "files"; newest and oldest are the timestamps of the first and last
// results returned ("" when there are none); reason, if any, says why the
// search stopped before reaching the limit.
func (s *searcher) more(noun string, total int, newest, oldest, reason string) *More {
	m := &More{Total: total}
	var hint []string
	if reason != "" {
		hint = append(hint, reason)
	}

	loc := s.opts.Location
	oldestTime, okOldest := tsTime(oldest)
	newestTime, okNewest := tsTime(newest)
	if okOldest && okNewest {
		oldestDay := oldestTime.In(loc).Format(dayLayout)
		next := oldestTime.In(loc).AddDate(0, 0, 1).Format(dayLayout)
		// The current upper bound: --before, or just past the newest
		// result. Asking for the same bound again would return the same
		// results.
		upper := s.days.before
		if upper == "" {
			upper = newestTime.In(loc).AddDate(0, 0, 1).Format(dayLayout)
		}
		if next < upper {
			m.Before = next
			hint = append(hint, fmt.Sprintf("Results are newest first: for older %s, repeat the search with --before %s (--before is exclusive; the results dated %s will come back again).", noun, next, oldestDay))
		} else {
			hint = append(hint, fmt.Sprintf("Every result returned is dated %s, so --before cannot page further: narrow the query to see the rest.", oldestDay))
		}
	}
	if s.limit < MaxLimit {
		hint = append(hint, fmt.Sprintf("Raising --limit (now %d, max %d) returns more at once.", s.limit, MaxLimit))
	}
	if len(hint) == 0 {
		hint = append(hint, fmt.Sprintf("More %s match than were returned: narrow the query.", noun))
	}
	m.Hint = strings.Join(hint, " ")
	return m
}
