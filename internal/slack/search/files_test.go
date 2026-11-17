package search

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func fileMatch(id, title, mimetype string, created int) map[string]any {
	f := slackFile(id, mimetype)
	f["title"], f["timestamp"] = title, created
	return f
}

func titles(res *Result) []string {
	var out []string
	for _, f := range res.Files {
		out = append(out, f.Title)
	}
	return out
}

// Port of "skips failed downloads and keeps later successful matches". A
// failed download no longer hides the file: it is reported with the
// reason, like the files of a message.
func TestSearchFilesKeepsGoingAfterFailedDownload(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F2"] = "ok"
	f.searchPages("search.files", 2, []any{
		fileMatch("F1", "Missing report", "text/plain", jan1+1),
		fileMatch("F2", "Working report", "text/plain", jan1),
	})

	opts := options(t, KindFiles, "report")
	opts.Limit, opts.ContentType = 5, ContentFile
	res := mustSearch(t, f, opts)

	if len(res.Files) != 2 {
		t.Fatalf("files = %+v", res.Files)
	}
	missing, working := res.Files[0], res.Files[1]
	if working.Title != "Working report" || working.Mimetype != "text/plain" || !strings.HasSuffix(working.Path, "/F2.txt") || working.Error != "" {
		t.Errorf("working = %+v", working)
	}
	if body, err := os.ReadFile(working.Path); err != nil || string(body) != "ok" {
		t.Errorf("downloaded %q, %v", body, err)
	}
	if missing.Title != "Missing report" || missing.Error != "Failed to download file (404)" || !strings.HasSuffix(missing.Path, "/F1.download-error.txt") {
		t.Errorf("missing = %+v", missing)
	}
	if missing.Permalink != "https://test.slack.com/files/U1/F1" {
		t.Errorf("permalink = %q", missing.Permalink)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "F1") {
		t.Errorf("warnings = %q", res.Warnings)
	}
	if res.MoreFiles != nil {
		t.Errorf("more = %+v", res.MoreFiles)
	}
	want := map[string]any{"query": "report", "count": 5, "page": 1, "highlight": false, "sort": "timestamp", "sort_dir": "desc"}
	if got := f.callsTo("search.files")[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("search.files call = %v, want %v", got, want)
	}
}

func TestSearchFilesContentTypeFilterKeepsPaging(t *testing.T) {
	f := newFake(t)
	f.searchPages("search.files", 6,
		[]any{
			fileMatch("F1", "a.pdf", "application/pdf", jan1+5*day),
			fileMatch("F2", "one.png", "image/png", jan1+4*day),
			map[string]any{"title": "no id"},
		},
		[]any{
			fileMatch("F2", "one.png", "image/png", jan1+4*day), // repeated across pages
			fileMatch("F3", "two.png", "image/png", jan1+3*day),
			fileMatch("F4", "three.png", "image/png", jan1+2*day),
		},
		[]any{fileMatch("F5", "four.png", "image/png", jan1)},
	)

	opts := options(t, KindFiles, "x")
	opts.Limit, opts.ContentType, opts.NoDownload = 2, ContentImage, true
	res := mustSearch(t, f, opts)

	if got, want := titles(res), []string{"one.png", "two.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	if n := len(f.callsTo("search.files")); n != 2 {
		t.Errorf("search.files calls = %d, want 2 (no page beyond the limit)", n)
	}
	if len(f.gets) != 0 || res.Files[0].Path != "" || res.Files[0].Permalink == "" {
		t.Errorf("NoDownload: gets = %v, file = %+v", f.gets, res.Files[0])
	}
	if res.MoreFiles == nil || res.MoreFiles.Total != 6 || res.MoreFiles.Before != "2026-01-05" {
		t.Errorf("more = %+v, want total 6 and before 2026-01-05", res.MoreFiles)
	}
}

func TestSearchFilesNeverSendsCredentialsOffSlack(t *testing.T) {
	f := newFake(t)
	f.searchPages("search.files", 2, []any{
		map[string]any{"id": "F1", "title": "Roadmap", "mimetype": "application/vnd.google-apps.document", "url_private": "https://docs.google.com/document/d/1", "timestamp": jan1},
		map[string]any{"id": "F2", "name": "nourl.bin"},
	})
	res := mustSearch(t, f, options(t, KindFiles, "roadmap"))

	if len(f.gets) != 0 {
		t.Fatalf("requests = %v", f.gets)
	}
	if got := res.Files[0]; !strings.Contains(got.Error, "Refusing to send Slack credentials") || got.Title != "Roadmap" {
		t.Errorf("external file = %+v", got)
	}
	if got := res.Files[1]; got.Error != "Slack gave no download URL for this file" || got.Title != "nourl.bin" {
		t.Errorf("file without URL = %+v", got)
	}
}

// Port of "continues after a failed download in files.list results".
func TestScanFilesKeepsGoingAfterFailedDownload(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F2"] = "ok"
	f.on("files.list", func(map[string]any) (map[string]any, error) {
		return map[string]any{
			"files": []any{
				fileMatch("F1", "Broken report", "text/plain", jan1+1),
				fileMatch("F2", "Healthy report", "text/plain", jan1),
			},
			"paging": map[string]any{"pages": 1},
		}, nil
	})

	opts := options(t, KindFiles, "report")
	opts.Channels, opts.Limit, opts.ContentType = []string{general}, 5, ContentFile
	res := mustSearch(t, f, opts)

	if len(res.Files) != 2 {
		t.Fatalf("files = %+v", res.Files)
	}
	if got := res.Files[1]; got.Title != "Healthy report" || got.Mimetype != "text/plain" || !strings.HasSuffix(got.Path, "/F2.txt") {
		t.Errorf("healthy = %+v", got)
	}
	if got := res.Files[0]; got.Title != "Broken report" || got.Error == "" {
		t.Errorf("broken = %+v", got)
	}
	want := map[string]any{"channel": general, "count": filesPageSize, "page": 1}
	if calls := f.callsTo("files.list"); len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Errorf("files.list calls = %v, want one %v", calls, want)
	}
}

func TestScanFilesPagesFiltersAndMerges(t *testing.T) {
	f := newFake(t)
	const other = "C87654321"
	f.on("files.list", func(p map[string]any) (map[string]any, error) {
		if p["channel"] == other {
			return map[string]any{"files": []any{
				fileMatch("F9", "Q4 Report (shared)", "application/pdf", jan1+9*day),
				fileMatch("F3", "q3 report", "application/pdf", jan1+3*day), // also in the first channel
			}, "paging": map[string]any{"pages": 1}}, nil
		}
		// No paging information: full pages follow each other until one
		// brings nothing new.
		full := func(page int, files ...any) map[string]any {
			for i := len(files); i < filesPageSize; i++ {
				files = append(files, fileMatch(fmt.Sprintf("FILLER%d%03d", page, i), "holiday.png", "image/png", jan1+day))
			}
			return map[string]any{"files": files}
		}
		if p["page"] == 1 {
			return full(1, fileMatch("F5", "Q5 REPORT", "application/pdf", jan1+5*day)), nil
		}
		return full(2,
			fileMatch("F3", "q3 report", "application/pdf", jan1+3*day),
			map[string]any{"id": "F2", "name": "report-by-name.pdf", "timestamp": jan1 + 2*day},
		), nil
	})

	opts := options(t, KindFiles, "report")
	opts.Channels, opts.NoDownload = []string{general, other}, true
	opts.User, opts.After, opts.Before = "U11111111", "2026-01-01", "2026-02-01"
	res := mustSearch(t, f, opts)

	want := []string{"Q4 Report (shared)", "Q5 REPORT", "q3 report", "report-by-name.pdf"}
	if got := titles(res); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %q, want %q", got, want)
	}
	if res.MoreFiles != nil {
		t.Errorf("more = %+v", res.MoreFiles)
	}
	var first map[string]any
	pages := 0
	for _, c := range f.callsTo("files.list") {
		if c["channel"] == general {
			pages++
			if c["page"] == 1 {
				first = c
			}
		}
	}
	if pages != 3 {
		t.Errorf("pages listed = %d, want 3 (it must stop at the page that repeats)", pages)
	}
	// Exclusive days, inclusive files.list bounds: 2026-01-02 00:00:00 to 2026-01-31 23:59:59 UTC.
	wantCall := map[string]any{"channel": general, "count": filesPageSize, "page": 1, "user": "U11111111", "ts_from": int64(1767312000), "ts_to": int64(1769903999)}
	if !reflect.DeepEqual(first, wantCall) {
		t.Errorf("files.list call = %v, want %v", first, wantCall)
	}

	opts.Limit = 2
	res = mustSearch(t, f, opts)
	if got := titles(res); !reflect.DeepEqual(got, want[:2]) {
		t.Errorf("limit 2: files = %q", got)
	}
	if res.MoreFiles == nil || res.MoreFiles.Before != "2026-01-07" {
		t.Errorf("limit 2: more = %+v, want before 2026-01-07", res.MoreFiles)
	}
}

func TestSearchAllRunsBothSearches(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F1"] = "ok"
	f.searchPages("search.messages", 1, []any{match(general, jan1, "hello")})
	f.searchPages("search.files", 1, []any{fileMatch("F1", "hello.txt", "text/plain", jan1)})
	f.history(message(jan1, "hello"))

	res := mustSearch(t, f, options(t, KindAll, "hello"))
	if len(res.Messages) != 1 || len(res.Files) != 1 {
		t.Errorf("result = %+v", res)
	}

	// Channel-scoped: both scans, the channel and the user resolved once.
	f = newFake(t)
	f.history(message(jan1, "hello"))
	f.on("files.list", func(map[string]any) (map[string]any, error) {
		return map[string]any{"files": []any{fileMatch("F1", "hello.txt", "text/plain", jan1)}}, nil
	})
	f.on("search.messages", func(map[string]any) (map[string]any, error) {
		return map[string]any{"messages": map[string]any{"matches": []any{match(general, jan1, "x")}}}, nil
	})
	opts := options(t, KindAll, "hello")
	opts.Channels, opts.NoDownload = []string{"#general"}, true
	res = mustSearch(t, f, opts)
	if len(res.Messages) != 1 || len(res.Files) != 1 {
		t.Errorf("scoped result = %+v", res)
	}
	if n := len(f.callsTo("search.messages")); n != 1 {
		t.Errorf("channel name lookups = %d, want 1", n)
	}
}
