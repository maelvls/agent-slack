package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// ChecksumsName is the release asset listing the SHA-256 of every archive.
//
// It, AssetName and BinaryName MUST agree with the "archives" and
// "checksum" sections of .goreleaser.yaml and with install.sh;
// TestReleaseNamingMatchesTooling fails when they drift apart.
const ChecksumsName = "checksums.txt"

// AssetName is the release archive holding the binary for a platform,
// e.g. slackcli_linux_amd64.tar.gz or slackcli_windows_arm64.zip.
func AssetName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s.%s", appinfo.Name, goos, goarch, ext)
}

// BinaryName is the name of the executable at the root of a release
// archive.
func BinaryName(goos string) string {
	if goos == "windows" {
		return appinfo.Name + ".exe"
	}
	return appinfo.Name
}

const (
	maxAPIResponse = 1 << 20   // releases/latest is a few kilobytes
	maxChecksums   = 1 << 20   // one line per archive
	maxArchive     = 256 << 20 // a release archive is ~10 MB
	maxBinary      = 512 << 20 // guards against a decompression bomb
)

// RateLimitError is returned when GitHub refuses a request because the
// (per IP address, unauthenticated) API quota is used up.
type RateLimitError struct {
	// RetryAfter is how long to wait, or zero when GitHub did not say.
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	msg := "GitHub API rate limit exceeded (unauthenticated requests are limited per IP address); "
	if e.RetryAfter > 0 {
		return msg + "try again in " + e.RetryAfter.Round(time.Second).String()
	}
	return msg + "try again later"
}

type release struct {
	tag     string // git tag, e.g. "v1.2.3"
	version string // canonical version, e.g. "1.2.3"
}

func (u *Updater) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", appinfo.UserAgent())
	return u.HTTPClient.Do(req)
}

// readLimited reads a whole body but fails, instead of truncating or
// exhausting memory, when it exceeds max bytes.
func readLimited(r io.Reader, max int64, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", what, err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes; refusing to use it", what, max)
	}
	return b, nil
}

// latestRelease returns the newest non-prerelease release of the repo.
func (u *Updater) latestRelease(ctx context.Context) (release, error) {
	resp, err := u.get(ctx, u.APIBaseURL+"/repos/"+u.Repo+"/releases/latest")
	if err != nil {
		return release{}, fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return release{}, fmt.Errorf("checking for updates: %s has no published release", u.Repo)
	case isRateLimited(resp):
		return release{}, &RateLimitError{RetryAfter: retryAfter(resp, u.Now())}
	default:
		return release{}, fmt.Errorf("checking for updates: GitHub answered HTTP %d", resp.StatusCode)
	}

	body, err := readLimited(resp.Body, maxAPIResponse, "the GitHub release")
	if err != nil {
		return release{}, err
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return release{}, fmt.Errorf("checking for updates: decoding the GitHub release: %w", err)
	}
	// The tag ends up in a URL and on the terminal: only accept a version.
	v, err := parseSemver(payload.TagName)
	if err != nil {
		return release{}, fmt.Errorf("checking for updates: latest release has tag %q, which is not a version", payload.TagName)
	}
	return release{tag: strings.TrimSpace(payload.TagName), version: v.String()}, nil
}

// isRateLimited recognises GitHub's primary (403 or 429 with an exhausted
// quota) and secondary (Retry-After) rate limits.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusForbidden &&
		(resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")
}

func retryAfter(resp *http.Response, now time.Time) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if d := time.Unix(reset, 0).Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// assetURL is the download URL of a release asset. It is built from the
// tag rather than taken from the API response so that the host is ours to
// choose.
func (u *Updater) assetURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", u.DownloadBaseURL, u.Repo, url.PathEscape(tag), url.PathEscape(name))
}

// downloadAsset fetches a release asset into memory.
func (u *Updater) downloadAsset(ctx context.Context, tag, name string, max int64) ([]byte, error) {
	resp, err := u.get(ctx, u.assetURL(tag, name))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("release %s has no asset %s (is %s/%s a supported platform?)", tag, name, u.GOOS, u.GOARCH)
	default:
		return nil, fmt.Errorf("downloading %s: HTTP %d", name, resp.StatusCode)
	}
	return readLimited(resp.Body, max, name)
}

// findChecksum returns the lower-case hex SHA-256 recorded for name in a
// sha256sum-style listing ("<hex>  <name>", optionally "*<name>").
func findChecksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			return "", fmt.Errorf("%s has a malformed SHA-256 for %s", ChecksumsName, name)
		}
		return sum, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", ChecksumsName, err)
	}
	return "", fmt.Errorf("%s has no entry for %s; refusing to install an unverified download", ChecksumsName, name)
}
