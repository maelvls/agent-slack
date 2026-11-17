package messagecmd

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/maelvls/slackcli/internal/slack/files"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// downloadConcurrency bounds simultaneous file downloads.
const downloadConcurrency = 4

var fileExtensionRe = regexp.MustCompile(`\.([A-Za-z0-9]{1,10})$`)

// inferFileExtension picks the extension of the local copy: files are
// saved as <file ID>.<ext> so that they can be opened with the right tool.
func inferFileExtension(f messages.FileSummary) string {
	mt, ft := strings.ToLower(f.Mimetype), strings.ToLower(f.Filetype)
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
	if m := fileExtensionRe.FindStringSubmatch(cmp.Or(f.Name, f.Title)); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// isCanvas reports whether f is a canvas (or one of its ancestors), which
// downloads as HTML and is converted to Markdown.
func isCanvas(f messages.FileSummary) bool {
	return f.Mode == "canvas" || f.Mode == "quip" || f.Mode == "docs"
}

// downloadMessageFiles saves the files attached to msgs locally and
// returns the outcome per file ID. A file that cannot be downloaded never
// fails the command: its outcome carries the error (also written next to
// the downloads, so that the output has a path to point at) and a warning
// goes to stderr. Only cancellation is returned as an error.
func (c *command) downloadMessageFiles(ctx context.Context, client *slackapi.Client, msgs []messages.Message) (map[string]files.DownloadResult, error) {
	var todo []messages.FileSummary
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, f := range m.Files {
			if !seen[f.ID] {
				seen[f.ID] = true
				todo = append(todo, f)
			}
		}
	}
	if len(todo) == 0 {
		return nil, nil
	}

	results := make([]files.DownloadResult, len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, downloadConcurrency)
	for i, f := range todo {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = c.downloadFile(ctx, client, f)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := make(map[string]files.DownloadResult, len(todo))
	for i, f := range todo {
		out[f.ID] = results[i]
		if !results[i].OK {
			c.rt.Hintf("Warning: skipping file %s: %s", f.ID, results[i].Error)
		}
	}
	return out, nil
}

// downloadFile downloads one file; failures of any kind end up in the
// result.
func (c *command) downloadFile(ctx context.Context, client *slackapi.Client, f messages.FileSummary) files.DownloadResult {
	var (
		res files.DownloadResult
		err error
	)
	switch {
	case isCanvas(f):
		res, err = c.downloadCanvas(ctx, client, f)
	case cmp.Or(f.URLPrivateDownload, f.URLPrivate) == "":
		res.Error = "Slack gave no download URL for this file (it may have been deleted, be hidden by the workspace's storage limit, or be hosted elsewhere)"
	default:
		name := f.ID
		if ext := inferFileExtension(f); ext != "" {
			name += "." + ext
		}
		res, err = files.TryDownload(ctx, client, cmp.Or(f.URLPrivateDownload, f.URLPrivate), files.DownloadOptions{
			DestDir:       c.downloadsDir,
			PreferredName: name,
		})
	}
	if err != nil {
		// A local failure (disk full, unwritable directory): the message
		// is still worth printing.
		res = files.DownloadResult{Error: err.Error()}
	}
	if !res.OK {
		if p, err := files.WriteDownloadErrorFile(c.downloadsDir, f.ID, res.Error); err == nil {
			res.Path = p
		}
	}
	return res
}

// downloadCanvas saves a canvas as <file ID>.md. Canvases change, so they
// are fetched again every time.
func (c *command) downloadCanvas(ctx context.Context, client *slackapi.Client, f messages.FileSummary) (files.DownloadResult, error) {
	url := cmp.Or(f.URLPrivate, f.URLPrivateDownload)
	if url == "" {
		return files.DownloadResult{Error: "Slack gave no download URL for this canvas"}, nil
	}
	htmlPath, err := files.Download(ctx, client, url, files.DownloadOptions{
		DestDir:       c.downloadsDir,
		PreferredName: f.ID + ".html",
		AllowHTML:     true,
		Overwrite:     true,
	})
	if err != nil {
		var de *files.DownloadError
		if !errors.As(err, &de) {
			return files.DownloadResult{}, err
		}
		res := files.DownloadResult{Error: de.Message, HTTPStatus: de.HTTPStatus}
		if errors.Is(err, files.ErrLoginPage) {
			res.Error = "Downloaded auth/login page instead of canvas content (token may be expired)"
		}
		return res, nil
	}
	// The HTML is only an intermediate: do not leave it behind.
	defer os.Remove(htmlPath)

	html, err := os.ReadFile(htmlPath)
	if err != nil {
		return files.DownloadResult{}, err
	}
	markdown := strings.TrimSpace(slacktext.HTMLToMarkdown(string(html)))
	mdPath := filepath.Join(filepath.Dir(htmlPath), files.SanitizeFilename(f.ID+".md"))
	if err := os.WriteFile(mdPath, []byte(markdown+"\n"), 0o600); err != nil {
		return files.DownloadResult{}, err
	}
	return files.DownloadResult{OK: true, Path: mdPath}, nil
}
