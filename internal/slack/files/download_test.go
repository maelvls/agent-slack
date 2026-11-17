package files

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// transport is a fake HTTP transport recording the requests it serves.
type transport struct {
	requests []*http.Request
	serve    func(req *http.Request) (*http.Response, error)
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req)
	return t.serve(req)
}

func response(status int, contentType, body string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func serving(status int, contentType, body string) *transport {
	return &transport{serve: func(*http.Request) (*http.Response, error) {
		return response(status, contentType, body), nil
	}}
}

func standardClient(tr *transport) *slackapi.Client {
	return slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "",
		slackapi.WithHTTPClient(&http.Client{Transport: tr}))
}

func browserClient(tr *transport) *slackapi.Client {
	return slackapi.New(slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-a+b"}, "https://acme.slack.com",
		slackapi.WithHTTPClient(&http.Client{Transport: tr}))
}

// failingBody fails after yielding some bytes.
type failingBody struct{ sent bool }

func (b *failingBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("stream failed")
}

func (*failingBody) Close() error { return nil }

func downloadError(t *testing.T, err error) *DownloadError {
	t.Helper()
	var de *DownloadError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v (%T), want *DownloadError", err, err)
	}
	return de
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDownloadWritesFile(t *testing.T) {
	dir := t.TempDir()
	tr := serving(200, "application/octet-stream", "hello world")
	got, err := Download(context.Background(), standardClient(tr), "https://files.slack.com/files/test.txt",
		DownloadOptions{DestDir: dir, PreferredName: "out.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "out.txt") {
		t.Errorf("path = %s", got)
	}
	if data, _ := os.ReadFile(got); string(data) != "hello world" {
		t.Errorf("content = %q", data)
	}
	if info, _ := os.Stat(got); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Errorf("leftover files: %v", names)
	}
	if h := tr.requests[0].Header; h.Get("Authorization") != "Bearer xoxb-test" || h.Get("Cookie") != "" || h.Get("User-Agent") != appinfo.UserAgent() {
		t.Errorf("headers = %v", h)
	}
}

func TestDownloadBrowserAuthHeaders(t *testing.T) {
	tr := serving(200, "image/png", "png")
	if _, err := Download(context.Background(), browserClient(tr), "https://files.slack.com/files-pri/T1-F1/a.png", DownloadOptions{DestDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	h := tr.requests[0].Header
	if h.Get("Authorization") != "Bearer xoxc-test" || h.Get("Cookie") != "d=xoxd-a%2Bb" || h.Get("Referer") != "https://app.slack.com/" {
		t.Errorf("headers = %v", h)
	}
}

func TestDownloadNamesFileAfterURL(t *testing.T) {
	dir := t.TempDir()
	got, err := Download(context.Background(), standardClient(serving(200, "", "x")), "https://files.slack.com/files-pri/T1-F1/report%20v2.pdf?t=xoxe", DownloadOptions{DestDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "report v2.pdf") {
		t.Errorf("path = %s", got)
	}
}

func TestDownloadUsesDefaultDownloadsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	got, err := Download(context.Background(), standardClient(serving(200, "", "x")), "https://files.slack.com/files/a.txt", DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(appinfo.DownloadsDir(), "a.txt") || !strings.HasPrefix(got, home) {
		t.Errorf("path = %s", got)
	}
}

func TestDownloadReturnsExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "cached.txt")
	if err := os.WriteFile(existing, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := serving(200, "", "fresh")
	client := standardClient(tr)
	got, err := Download(context.Background(), client, "https://files.slack.com/files/cached.txt", DownloadOptions{DestDir: dir, PreferredName: "cached.txt"})
	if err != nil || got != existing {
		t.Fatalf("Download = %q, %v", got, err)
	}
	if len(tr.requests) != 0 {
		t.Errorf("made %d requests, want none", len(tr.requests))
	}

	// Mutable content (canvases) must be refreshable.
	if _, err := Download(context.Background(), client, "https://files.slack.com/files/cached.txt", DownloadOptions{DestDir: dir, PreferredName: "cached.txt", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "fresh" || len(tr.requests) != 1 {
		t.Errorf("content = %q after %d requests", data, len(tr.requests))
	}
}

func TestDownloadHTTPErrors(t *testing.T) {
	for _, status := range []int{401, 404, 500} {
		dir := t.TempDir()
		_, err := Download(context.Background(), standardClient(serving(status, "", "")), "https://files.slack.com/files/missing.txt", DownloadOptions{DestDir: dir})
		de := downloadError(t, err)
		if de.HTTPStatus != status || !strings.Contains(de.Message, "Failed to download file") {
			t.Errorf("status %d: error = %+v", status, de)
		}
		if names := dirEntries(t, dir); len(names) != 0 {
			t.Errorf("status %d: files written: %v", status, names)
		}
	}
}

func TestDownloadHTMLResponses(t *testing.T) {
	const loginPage = "<html><head><title>Sign in | Slack</title></head><body>Sign in</body></html>"
	const canvas = "<html><body><h1>Roadmap</h1></body></html>"
	tests := []struct {
		name      string
		body      string
		allowHTML bool
		wantLogin bool
		wantMsg   string // "" means success
	}{
		{"sign-in page", loginPage, false, true, "sign-in page"},
		// With allowHtml the TypeScript wrote the sign-in page to disk and
		// then served it from cache forever.
		{"sign-in page even when HTML is allowed", loginPage, true, true, "sign-in page"},
		{"other HTML", canvas, false, false, "Downloaded HTML instead of file"},
		{"HTML allowed", canvas, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			got, err := Download(context.Background(), standardClient(serving(200, "text/html; charset=utf-8", tt.body)),
				"https://files.slack.com/files/F1", DownloadOptions{DestDir: dir, PreferredName: "F1.html", AllowHTML: tt.allowHTML})
			if tt.wantMsg == "" {
				if err != nil {
					t.Fatal(err)
				}
				if data, _ := os.ReadFile(got); string(data) != tt.body {
					t.Errorf("content = %q", data)
				}
				return
			}
			de := downloadError(t, err)
			if !strings.Contains(de.Message, tt.wantMsg) || de.HTTPStatus != 200 {
				t.Errorf("error = %+v", de)
			}
			if errors.Is(err, ErrLoginPage) != tt.wantLogin {
				t.Errorf("errors.Is(err, ErrLoginPage) = %v, want %v", !tt.wantLogin, tt.wantLogin)
			}
			if names := dirEntries(t, dir); len(names) != 0 {
				t.Errorf("files written: %v", names)
			}
		})
	}
}

func TestDownloadNetworkError(t *testing.T) {
	tr := &transport{serve: func(*http.Request) (*http.Response, error) { return nil, errors.New("fetch failed") }}
	_, err := Download(context.Background(), standardClient(tr), "https://files.slack.com/files/unreachable.txt", DownloadOptions{DestDir: t.TempDir()})
	if de := downloadError(t, err); !strings.Contains(de.Message, "Network error") || de.HTTPStatus != 0 {
		t.Errorf("error = %+v", de)
	}
}

func TestDownloadBodyReadErrors(t *testing.T) {
	for _, contentType := range []string{"text/html", "application/octet-stream"} {
		dir := t.TempDir()
		tr := &transport{serve: func(*http.Request) (*http.Response, error) {
			resp := response(200, contentType, "")
			resp.Body = &failingBody{}
			return resp, nil
		}}
		_, err := Download(context.Background(), standardClient(tr), "https://files.slack.com/files/file.bin", DownloadOptions{DestDir: dir, AllowHTML: true})
		if de := downloadError(t, err); !strings.Contains(de.Message, "Failed to read download response body") || de.HTTPStatus != 200 {
			t.Errorf("%s: error = %+v", contentType, de)
		}
		// No partial file may be left behind to be mistaken for the real one.
		if names := dirEntries(t, dir); len(names) != 0 {
			t.Errorf("%s: files left behind: %v", contentType, names)
		}
	}
}

func TestDownloadCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &transport{serve: func(req *http.Request) (*http.Response, error) {
		cancel()
		return nil, req.Context().Err()
	}}
	_, err := Download(ctx, standardClient(tr), "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	var de *DownloadError
	if errors.As(err, &de) {
		t.Error("a cancellation must not be reported as a download failure")
	}
}

func TestDownloadRefusesNonSlackHosts(t *testing.T) {
	for _, u := range []string{
		"https://docs.google.com/document/d/abc",       // external file
		"https://files.slack.com.evil.example/x",       // look-alike
		"https://evilslack.com/x",                      // look-alike
		"http://files.slack.com/files/a.txt",           // not https
		"https://files.slack.com@evil.example/a.txt",   // host in userinfo
		"https://user:pw@files.slack.com/files/a.txt",  // userinfo
		"file:///etc/passwd",                           // not http at all
		"https://169.254.169.254/latest/meta-data/iam", // internal address
	} {
		tr := serving(200, "", "secret")
		_, err := Download(context.Background(), standardClient(tr), u, DownloadOptions{DestDir: t.TempDir()})
		if !errors.Is(err, ErrNotSlackHost) {
			t.Errorf("%s: err = %v, want ErrNotSlackHost", u, err)
		}
		if len(tr.requests) != 0 {
			t.Errorf("%s: a request was sent", u)
		}
		if err != nil && strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: password leaked in %q", u, err)
		}
	}
}

func TestIsSlackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"files.slack.com": true, "slack.com": true, "acme.enterprise.slack.com": true, "FILES.SLACK.COM": true,
		"a.slack-edge.com": true, "slack-files.com": true, "files.slack-gov.com": true,
		"evilslack.com": false, "slack.com.evil.example": false, "example.com": false, "": false,
	} {
		if IsSlackHost(host) != want {
			t.Errorf("IsSlackHost(%q) = %v", host, !want)
		}
	}
}

func TestDownloadRedirects(t *testing.T) {
	redirecting := func(location string) *transport {
		return &transport{serve: func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "files.slack.com" {
				resp := response(302, "", "")
				resp.Header.Set("Location", location)
				return resp, nil
			}
			return response(200, "application/octet-stream", "payload"), nil
		}}
	}

	t.Run("to another Slack host keeps credentials", func(t *testing.T) {
		tr := redirecting("https://files-origin.slack.com/files/a.txt")
		if _, err := Download(context.Background(), browserClient(tr), "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if h := tr.requests[1].Header; h.Get("Authorization") != "Bearer xoxc-test" || h.Get("Cookie") == "" {
			t.Errorf("headers after redirect = %v", h)
		}
	})

	t.Run("to a foreign host drops credentials", func(t *testing.T) {
		tr := redirecting("https://cdn.example.net/a.txt")
		if _, err := Download(context.Background(), browserClient(tr), "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		h := tr.requests[1].Header
		for _, name := range []string{"Authorization", "Cookie", "Referer"} {
			if h.Get(name) != "" {
				t.Errorf("%s sent to a foreign host: %q", name, h.Get(name))
			}
		}
	})

	t.Run("to plain http drops credentials", func(t *testing.T) {
		tr := redirecting("http://files-origin.slack.com/files/a.txt")
		if _, err := Download(context.Background(), standardClient(tr), "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if got := tr.requests[1].Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization sent over http: %q", got)
		}
	})

	t.Run("loops are cut", func(t *testing.T) {
		tr := &transport{serve: func(*http.Request) (*http.Response, error) {
			resp := response(302, "", "")
			resp.Header.Set("Location", "https://files.slack.com/files/a.txt")
			return resp, nil
		}}
		_, err := Download(context.Background(), standardClient(tr), "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()})
		if de := downloadError(t, err); !strings.Contains(de.Message, "redirects") {
			t.Errorf("error = %+v", de)
		}
		if len(tr.requests) > maxRedirects+1 {
			t.Errorf("%d requests", len(tr.requests))
		}
	})
}

func TestDownloadDoesNotChangeTheSharedHTTPClient(t *testing.T) {
	hc := &http.Client{Transport: serving(200, "", "x")}
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "", slackapi.WithHTTPClient(hc))
	if _, err := Download(context.Background(), client, "https://files.slack.com/files/a.txt", DownloadOptions{DestDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if hc.CheckRedirect != nil {
		t.Error("the API client's redirect policy was modified")
	}
}

func TestDownloadCannotEscapeDestDir(t *testing.T) {
	for _, name := range []string{"..", "../../etc/passwd", "/etc/passwd", `..\..\boot.ini`, ".", "a/../../b"} {
		root := t.TempDir()
		dir := filepath.Join(root, "downloads")
		got, err := Download(context.Background(), standardClient(serving(200, "", "x")), "https://files.slack.com/files/a.txt",
			DownloadOptions{DestDir: dir, PreferredName: name})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if filepath.Dir(got) != dir {
			t.Errorf("%q: wrote %s, outside %s", name, got, dir)
		}
		if info, err := os.Stat(got); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%q: %s is not a regular file (%v)", name, got, err)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	long := strings.Repeat("é", 300) + ".png"
	tests := map[string]string{
		"report.pdf":             "report.pdf",
		`a\b/c<d>e:f"g|h?i*j`:    "a_b_c_d_e_f_g_h_i_j",
		"":                       "file",
		"   ":                    "file",
		".":                      "file",
		"..":                     "file",
		".bashrc":                "_bashrc",
		"../x":                   "__x",
		"tab\there\x00nul\x7f":   "tab_here_nul_",
		"  spaced name.txt  ":    "spaced name.txt",
		"bad\xffutf8.txt":        "bad_utf8.txt",
		"F0123ABCD.download.txt": "F0123ABCD.download.txt",
	}
	for in, want := range tests {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	got := SanitizeFilename(long)
	if len(got) > maxFileNameLen || !strings.HasSuffix(got, ".png") || strings.ToValidUTF8(got, "") != got {
		t.Errorf("long name → %d bytes %q", len(got), got)
	}
}

func TestTryDownload(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		res, err := TryDownload(ctx, standardClient(serving(200, "", "content")), "https://files.slack.com/files/good.txt", DownloadOptions{DestDir: dir, PreferredName: "good.txt"})
		if err != nil {
			t.Fatal(err)
		}
		if want := (DownloadResult{OK: true, Path: filepath.Join(dir, "good.txt")}); res != want {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("HTTP 404", func(t *testing.T) {
		res, err := TryDownload(ctx, standardClient(serving(404, "", "")), "https://files.slack.com/files/missing.txt", DownloadOptions{DestDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if want := (DownloadResult{Error: "Failed to download file (404)", HTTPStatus: 404}); res != want {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("network error", func(t *testing.T) {
		tr := &transport{serve: func(*http.Request) (*http.Response, error) { return nil, errors.New("fetch failed") }}
		res, err := TryDownload(ctx, standardClient(tr), "https://files.slack.com/files/down.txt", DownloadOptions{DestDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK || !strings.Contains(res.Error, "Network error") {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("external file", func(t *testing.T) {
		res, err := TryDownload(ctx, standardClient(serving(200, "", "")), "https://docs.google.com/document/d/abc", DownloadOptions{DestDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK || !strings.Contains(res.Error, "docs.google.com") {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("disk errors propagate", func(t *testing.T) {
		// A file where a directory is expected.
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := TryDownload(ctx, standardClient(serving(200, "", "content")), "https://files.slack.com/files/file.txt", DownloadOptions{DestDir: filepath.Join(blocker, "subdir")})
		if err == nil {
			t.Error("want an error")
		}
	})
}

func TestWriteDownloadErrorFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	got, err := WriteDownloadErrorFile(dir, "F1", "Failed to download file (404)")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "F1.download-error.txt") {
		t.Errorf("path = %s", got)
	}
	if data, _ := os.ReadFile(got); string(data) != "Failed to download file (404)\n" {
		t.Errorf("content = %q", data)
	}
	if info, _ := os.Stat(got); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", info.Mode().Perm())
	}

	// A hostile file ID stays inside the directory.
	got, err = WriteDownloadErrorFile(dir, "../../F2", "nope")
	if err != nil || filepath.Dir(got) != dir {
		t.Errorf("path = %s, err = %v", got, err)
	}
}

func TestLooksLikeLoginPage(t *testing.T) {
	for html, want := range map[string]bool{
		"<html><head><title>Sign in</title></head></html>":    true,
		"<TITLE>Slack | Sign  In</TITLE>":                     true,
		`<form id="x" action="/signin" method="post">`:        true,
		`<div data-qa="signin_domain_input">`:                 true,
		"<html><body><h1>How to sign in</h1></body></html>":   false,
		"<html><head><title>Roadmap</title></head></html>":    false,
		"<html><body><form action='/search'></form></body>":   false,
		"plain text mentioning Sign in":                       false,
		"<html><head><title>Designing the API</title></head>": false,
	} {
		if LooksLikeLoginPage([]byte(html)) != want {
			t.Errorf("LooksLikeLoginPage(%q) = %v", html, !want)
		}
	}
}
