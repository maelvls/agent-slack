package canvas

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/files"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	// DefaultMaxChars is how much Markdown the CLI includes by default.
	DefaultMaxChars = 20000
	// maxHTMLBytes bounds the canvas HTML held in memory.
	maxHTMLBytes = 64 << 20
	// loginSniffLen is how much of the HTML is searched for Slack's
	// sign-in form.
	loginSniffLen = 64 << 10
)

// Fetched is a canvas converted to Markdown, in the shape the CLI prints.
type Fetched struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Markdown string `json:"markdown"`
	// Truncated is set when Markdown was cut; TotalChars is then the
	// length of the whole document.
	Truncated  bool `json:"truncated,omitempty"`
	TotalChars int  `json:"total_chars,omitempty"`
}

// FetchResult is the result of Fetch.
type FetchResult struct {
	Canvas Fetched `json:"canvas"`
}

// Fetch downloads a canvas and converts it to Markdown, keeping at most
// maxChars characters (negative: no limit). The canvas is always fetched
// afresh and never written to disk: canvases change, and a cached copy
// would be read back stale after an edit.
//
// When Slack serves its sign-in page instead of the canvas, the error
// wraps files.ErrLoginPage.
func Fetch(ctx context.Context, client *slackapi.Client, canvasID string, maxChars int) (FetchResult, error) {
	info, err := client.API(ctx, "files.info", map[string]any{"file": canvasID})
	if err != nil {
		hint := ""
		if slackapi.IsErrorCode(err, "file_not_found", "file_deleted") {
			hint = " (check the canvas ID, and that --workspace selects the workspace it lives in)"
		}
		return FetchResult{}, fmt.Errorf("looking up canvas %s: %w%s", canvasID, err, hint)
	}
	file := jsonx.Map(info["file"])
	if file == nil {
		return FetchResult{}, fmt.Errorf("canvas %s not found (files.info returned no file)", canvasID)
	}
	title := strings.TrimSpace(jsonx.Str(file["title"]))
	if title == "" {
		title = strings.TrimSpace(jsonx.Str(file["name"]))
	}
	downloadURL := jsonx.Str(file["url_private_download"])
	if downloadURL == "" {
		downloadURL = jsonx.Str(file["url_private"])
	}
	if downloadURL == "" {
		return FetchResult{}, fmt.Errorf("canvas %s has no download URL", canvasID)
	}

	html, err := download(ctx, client, downloadURL)
	if err != nil {
		return FetchResult{}, fmt.Errorf("downloading canvas %s: %w", canvasID, err)
	}
	if !utf8.Valid(html) {
		return FetchResult{}, fmt.Errorf("file %s is not a canvas (file type %q): its content is not text", canvasID, jsonx.Str(file["filetype"]))
	}

	out := Fetched{ID: canvasID, Title: title, Markdown: strings.TrimSpace(slacktext.HTMLToMarkdown(string(html)))}
	if total := utf8.RuneCountInString(out.Markdown); maxChars >= 0 && total > maxChars {
		out.Markdown = string([]rune(out.Markdown)[:maxChars]) + "\n…"
		out.Truncated, out.TotalChars = true, total
	}
	return FetchResult{Canvas: out}, nil
}

// download returns the HTML of a canvas.
func download(ctx context.Context, client *slackapi.Client, rawURL string) ([]byte, error) {
	resp, err := files.Get(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &files.DownloadError{Message: fmt.Sprintf("Slack answered HTTP %d", resp.StatusCode), HTTPStatus: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	if len(body) > maxHTMLBytes {
		return nil, fmt.Errorf("the file is larger than %d MiB (not a canvas?)", maxHTMLBytes>>20)
	}
	if files.LooksLikeLoginPage(body[:min(len(body), loginSniffLen)]) {
		return nil, &files.DownloadError{
			Message:    fmt.Sprintf("Slack returned its sign-in page instead of the canvas: the credentials are expired or cannot access it (re-import them with `%s auth import-desktop` or another `auth` command)", appinfo.Name),
			HTTPStatus: resp.StatusCode,
			Err:        files.ErrLoginPage,
		}
	}
	return body, nil
}
