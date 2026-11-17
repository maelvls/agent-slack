package search

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/maelvls/slackcli/internal/slack/files"
)

// download is one file to fetch.
type download struct {
	id string
	// url is the private download URL; "" when Slack gave none.
	url string
	// name is the file name to save as.
	name string
}

// download fetches files a few at a time and returns the outcome of each,
// keyed by file ID. A file listed several times is fetched once. A failed
// download is an outcome, not an error: the reason is recorded in an
// error file, as `message get` does. With Options.NoDownload nothing is
// fetched and every outcome is a success without a path.
func (s *searcher) download(ctx context.Context, items []download) (map[string]files.DownloadResult, error) {
	results := make(map[string]files.DownloadResult, len(items))
	var unique []download
	for _, it := range items {
		if _, dup := results[it.id]; !dup {
			results[it.id] = files.DownloadResult{OK: true}
			unique = append(unique, it)
		}
	}
	if s.opts.NoDownload {
		return results, nil
	}

	var mu sync.Mutex
	err := forEach(ctx, len(unique), downloadConcurrency, func(ctx context.Context, i int) error {
		it := unique[i]
		res := files.DownloadResult{Error: "Slack gave no download URL for this file"}
		if it.url != "" {
			var err error
			res, err = files.TryDownload(ctx, s.client, it.url, files.DownloadOptions{DestDir: s.opts.DownloadsDir, PreferredName: it.name})
			if err != nil {
				return fmt.Errorf("downloading file %s: %w", it.id, err)
			}
		}
		if !res.OK {
			path, err := files.WriteDownloadErrorFile(s.opts.DownloadsDir, it.id, res.Error)
			if err != nil {
				return fmt.Errorf("recording why file %s could not be downloaded: %w", it.id, err)
			}
			res.Path = path
			s.warnf("file %s could not be downloaded: %s", it.id, res.Error)
		}
		mu.Lock()
		results[it.id] = res
		mu.Unlock()
		return nil
	})
	return results, err
}

// downloadName names a download after the file ID, which is unique and
// safe, with an extension that tells the reader what the file is.
func downloadName(id, mimetype, filetype, name, title string) string {
	if ext := inferExt(mimetype, filetype, name, title); ext != "" {
		return id + "." + ext
	}
	return id
}

var extRe = regexp.MustCompile(`\.([A-Za-z0-9]{1,10})$`)

// inferExt picks a file extension (without the dot) from the media type,
// Slack's file type, or failing those the extension of the name or title.
// It returns "" when none can be told.
func inferExt(mimetype, filetype, name, title string) string {
	mt, ft := strings.ToLower(mimetype), strings.ToLower(filetype)
	switch {
	case mt == "image/png" || ft == "png":
		return "png"
	case mt == "image/jpeg" || mt == "image/jpg" || ft == "jpg" || ft == "jpeg":
		return "jpg"
	case mt == "image/webp" || ft == "webp":
		return "webp"
	case mt == "image/gif" || ft == "gif":
		return "gif"
	case mt == "text/plain" || ft == "text":
		return "txt"
	case mt == "text/markdown" || ft == "markdown" || ft == "md":
		return "md"
	case mt == "application/json" || ft == "json":
		return "json"
	}
	if name == "" {
		name = title
	}
	if m := extRe.FindStringSubmatch(name); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}
