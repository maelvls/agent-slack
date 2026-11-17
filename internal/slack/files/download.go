// Package files downloads Slack-hosted files (message attachments,
// snippets, canvases) with the client's credentials.
package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	maxRedirects = 5
	// sniffLen is how much of an HTML response is inspected (and quoted in
	// errors) to recognise Slack's sign-in page.
	sniffLen = 64 << 10
	// maxFileNameLen keeps names well under the 255-byte limit of common
	// file systems.
	maxFileNameLen = 200
)

var (
	// ErrLoginPage means Slack answered with its sign-in page instead of
	// the file: the credentials are expired or cannot access the file.
	ErrLoginPage = errors.New("Slack returned its sign-in page instead of the file")
	// ErrNotSlackHost means the URL is not hosted by Slack (for instance
	// the url_private of an external Google Drive file); credentials are
	// never sent there.
	ErrNotSlackHost = errors.New("not a Slack-hosted URL")
)

// DownloadError is a failed download: an HTTP error status, a network
// failure, a sign-in page (ErrLoginPage) or a URL that credentials must
// not be sent to (ErrNotSlackHost). Local failures (creating the
// directory, writing the file) are returned as plain errors instead.
type DownloadError struct {
	// Message is the description recorded in DownloadResult.Error.
	Message string
	// HTTPStatus is the response status, 0 when there was no response.
	HTTPStatus int
	// Err is the underlying cause, if any (see ErrLoginPage,
	// ErrNotSlackHost).
	Err error
}

func (e *DownloadError) Error() string { return e.Message }

func (e *DownloadError) Unwrap() error { return e.Err }

// DownloadResult is the outcome of one attempted download.
type DownloadResult struct {
	OK bool `json:"ok"`
	// Path is the downloaded file, or for a failure the error file written
	// by WriteDownloadErrorFile, if any.
	Path string `json:"path,omitempty"`
	// Error and HTTPStatus describe a failure.
	Error      string `json:"error,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// DownloadOptions tunes Download.
type DownloadOptions struct {
	// DestDir is the directory to download into; "" means
	// appinfo.EnsureDownloadsDir(). It is created (0700) if missing.
	DestDir string
	// PreferredName is the file name to use; "" means the last element of
	// the URL path. It is sanitised with SanitizeFilename either way.
	PreferredName string
	// AllowHTML accepts a text/html response, which is otherwise taken to
	// be an error page. Canvases download as HTML. Slack's sign-in page is
	// rejected regardless.
	AllowHTML bool
	// Overwrite downloads again even if the destination file exists. By
	// default an existing file is returned as-is, which is right for
	// immutable attachments named after their file ID but not for content
	// that can change, such as canvases.
	Overwrite bool
}

// IsSlackHost reports whether host belongs to Slack, i.e. whether Slack
// credentials may be sent to it.
func IsSlackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range []string{"slack.com", "slack-edge.com", "slack-files.com", "slack-gov.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func checkSlackURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || !IsSlackHost(u.Hostname()) {
		return &DownloadError{
			Message: fmt.Sprintf("Refusing to send Slack credentials to %s: not an https URL on a Slack host (external file? open the link directly)", u.Redacted()),
			Err:     ErrNotSlackHost,
		}
	}
	return nil
}

// Get issues an authenticated GET for a Slack-hosted URL and returns the
// response whatever its status; the caller closes the body. Credentials
// are only ever sent over https to Slack-owned hosts: any other URL fails
// with ErrNotSlackHost, and a redirect that leaves Slack is followed
// without credentials.
func Get(ctx context.Context, client *slackapi.Client, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, &DownloadError{Message: fmt.Sprintf("Invalid file URL %q", rawURL), Err: err}
	}
	if err := checkSlackURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &DownloadError{Message: fmt.Sprintf("Invalid file URL %q", rawURL), Err: err}
	}
	authorize(client, req)

	// A copy, so that the redirect policy does not leak into the API
	// client. net/http already drops Authorization and Cookie when a
	// redirect leaves the original domain; this policy states the rule
	// explicitly for both directions (files.slack.com → another Slack host
	// keeps credentials, anything else never has them).
	hc := *client.HTTPClient()
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		for _, h := range []string{"Authorization", "Cookie", "Referer"} {
			next.Header.Del(h)
		}
		if checkSlackURL(next.URL) == nil {
			authorize(client, next)
		}
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &DownloadError{Message: "Network error: " + err.Error(), Err: err}
	}
	return resp, nil
}

func authorize(client *slackapi.Client, req *http.Request) {
	client.Authorize(req)
	if client.Auth().IsBrowser() {
		req.Header.Set("Referer", "https://app.slack.com/")
	}
}

// Download fetches a Slack-hosted file into opts.DestDir and returns its
// absolute path. The file is written atomically with mode 0600. Unless
// opts.Overwrite is set, a file that already exists is returned without
// any request.
//
// Failures to fetch are *DownloadError (use errors.Is with ErrLoginPage /
// ErrNotSlackHost for the cause); see TryDownload to get them as a value.
func Download(ctx context.Context, client *slackapi.Client, rawURL string, opts DownloadOptions) (string, error) {
	dir := opts.DestDir
	if dir == "" {
		d, err := appinfo.EnsureDownloadsDir()
		if err != nil {
			return "", fmt.Errorf("creating the downloads directory: %w", err)
		}
		dir = d
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the download directory: %w", err)
	}

	name := opts.PreferredName
	if name == "" {
		if u, err := url.Parse(rawURL); err == nil {
			name = path.Base(u.Path)
		}
	}
	dest := filepath.Join(dir, SanitizeFilename(name))
	if !opts.Overwrite {
		if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
			return dest, nil
		}
	}

	resp, err := Get(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &DownloadError{Message: fmt.Sprintf("Failed to download file (%d)", resp.StatusCode), HTTPStatus: resp.StatusCode}
	}

	body := io.Reader(resp.Body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		head, err := io.ReadAll(io.LimitReader(resp.Body, sniffLen))
		if err != nil {
			return "", readError(ctx, resp, err)
		}
		if LooksLikeLoginPage(head) {
			return "", &DownloadError{
				Message:    "Downloaded Slack's sign-in page instead of the file: the credentials are expired or cannot access it (re-import them with `" + appinfo.Name + " auth import-desktop` or another `auth` command)",
				HTTPStatus: resp.StatusCode,
				Err:        ErrLoginPage,
			}
		}
		if !opts.AllowHTML {
			return "", &DownloadError{
				Message:    "Downloaded HTML instead of file (auth likely failed). First bytes: " + strconv.Quote(firstRunes(head, 120)),
				HTTPStatus: resp.StatusCode,
			}
		}
		body = io.MultiReader(bytes.NewReader(head), resp.Body)
	}

	if err := writeAtomic(dest, body); err != nil {
		var re *bodyReadError
		if errors.As(err, &re) {
			return "", readError(ctx, resp, re.err)
		}
		return "", fmt.Errorf("writing %s: %w", dest, err)
	}
	return dest, nil
}

func readError(ctx context.Context, resp *http.Response, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &DownloadError{Message: "Failed to read download response body: " + err.Error(), HTTPStatus: resp.StatusCode, Err: err}
}

// bodyReadError marks a failure of the network side of a copy, as opposed
// to the disk side.
type bodyReadError struct{ err error }

func (e *bodyReadError) Error() string { return e.err.Error() }

type bodyReader struct{ r io.Reader }

func (b bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = &bodyReadError{err}
	}
	return n, err
}

// writeAtomic streams body to a temporary file next to dest and renames it
// into place, so that an interrupted or concurrent download never leaves a
// partial file that a later run would take for a finished one.
func writeAtomic(dest string, body io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.part") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := io.Copy(tmp, bodyReader{body}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// TryDownload is Download with fetch failures (*DownloadError) reported in
// the result instead of as an error. The returned error is only non-nil
// for local failures (disk, cancellation), which should abort the command.
func TryDownload(ctx context.Context, client *slackapi.Client, rawURL string, opts DownloadOptions) (DownloadResult, error) {
	p, err := Download(ctx, client, rawURL, opts)
	if err == nil {
		return DownloadResult{OK: true, Path: p}, nil
	}
	var de *DownloadError
	if errors.As(err, &de) {
		return DownloadResult{Error: de.Message, HTTPStatus: de.HTTPStatus}, nil
	}
	return DownloadResult{}, err
}

// WriteDownloadErrorFile records why a file could not be downloaded in
// <destDir>/<fileID>.download-error.txt (mode 0600) and returns its path,
// so that the message output can still point at something readable.
// destDir "" means appinfo.EnsureDownloadsDir().
func WriteDownloadErrorFile(destDir, fileID, message string) (string, error) {
	if destDir == "" {
		d, err := appinfo.EnsureDownloadsDir()
		if err != nil {
			return "", fmt.Errorf("creating the downloads directory: %w", err)
		}
		destDir = d
	}
	dir, err := filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, SanitizeFilename(fileID+".download-error.txt"))
	if err := writeAtomic(dest, strings.NewReader(message+"\n")); err != nil {
		return "", err
	}
	return dest, nil
}

// The word boundaries keep a document titled "Designing …" from being
// taken for the sign-in page.
var loginPageRe = regexp.MustCompile(`(?i)<form[^>]+signin|data-qa="signin|<title>[^<]*\bSign\s*in\b`)

// LooksLikeLoginPage reports whether html is Slack's sign-in page, which
// Slack serves with status 200 in place of a file when the credentials are
// not accepted.
func LooksLikeLoginPage(html []byte) bool { return loginPageRe.Match(html) }

func firstRunes(b []byte, n int) string {
	r := []rune(string(b))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// SanitizeFilename makes name safe to create inside a directory: path
// separators, characters that are special on Windows and control
// characters become "_", "." and ".." (and leading dots in general) cannot
// escape or hide, and over-long names are shortened keeping their
// extension. An unusable name becomes "file".
func SanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`\/<>:"|?*`, r), r < 0x20, r == 0x7f:
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	name = strings.TrimSpace(name)
	if trimmed := strings.TrimLeft(name, "."); trimmed != name {
		name = "_" + trimmed
	}
	if name == "" || name == "_" {
		return "file"
	}
	if len(name) > maxFileNameLen {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := name[:maxFileNameLen-len(ext)]
		// Do not cut a multi-byte character in half.
		name = strings.ToValidUTF8(stem, "") + ext
	}
	return name
}
