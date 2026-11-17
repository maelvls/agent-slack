// Package slackapi is a minimal Slack Web API client that works with both
// standard tokens (xoxb/xoxp) and browser session credentials (xoxc token +
// xoxd "d" cookie). Responses are returned as loosely-typed JSON; see
// package jsonx for accessors.
package slackapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// AuthType distinguishes standard tokens from browser session credentials.
type AuthType string

const (
	AuthStandard AuthType = "standard"
	AuthBrowser  AuthType = "browser"
)

// Auth is a set of Slack credentials. For AuthStandard only Token is set;
// for AuthBrowser XoxcToken and XoxdCookie are set. XoxdCookie is stored
// URL-decoded.
type Auth struct {
	Type       AuthType `json:"auth_type"`
	Token      string   `json:"token,omitempty"`
	XoxcToken  string   `json:"xoxc_token,omitempty"`
	XoxdCookie string   `json:"xoxd_cookie,omitempty"`
}

// IsBrowser reports whether these are browser session credentials.
func (a Auth) IsBrowser() bool { return a.Type == AuthBrowser }

// BearerToken is the token to present, whatever the auth type.
func (a Auth) BearerToken() string {
	if a.IsBrowser() {
		return a.XoxcToken
	}
	return a.Token
}

// Handler serves API calls in place of HTTP; used by tests (see NewFake).
type Handler func(ctx context.Context, method string, params map[string]any) (map[string]any, error)

const (
	defaultAPITimeout  = 20 * time.Second
	defaultBaseURL     = "https://slack.com"
	maxRateLimitDelay  = 30 * time.Second
	maxRateLimitRetry  = 3
	maxResponseBodyLen = 64 << 20
)

// Client calls Slack Web API methods.
type Client struct {
	auth         Auth
	workspaceURL string
	baseURL      string
	httpClient   *http.Client
	handler      Handler
	sleep        func(ctx context.Context, d time.Duration) error
	// loopbackForTesting lets credentials go to a loopback address (see
	// WithLoopbackWorkspaceForTesting).
	loopbackForTesting bool
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (tests, proxies).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithBaseURL overrides https://slack.com for standard-token calls (tests).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHandler short-circuits API and APIMultipart to fn (tests).
func WithHandler(fn Handler) Option { return func(c *Client) { c.handler = fn } }

// WithSleep replaces the rate-limit back-off sleep (tests).
func WithSleep(fn func(ctx context.Context, d time.Duration) error) Option {
	return func(c *Client) { c.sleep = fn }
}

// WithLoopbackWorkspaceForTesting is for tests only: it lets a client send
// its credentials to a workspace URL on a loopback address (an
// net/http/httptest server such as http://127.0.0.1:1234), over http or
// https. Without it, browser credentials are only ever sent to an https
// Slack host. It never allows a non-loopback host, and nothing in the CLI
// (flag, environment variable, configuration) can turn it on.
func WithLoopbackWorkspaceForTesting() Option {
	return func(c *Client) { c.loopbackForTesting = true }
}

// New returns a client for auth. workspaceURL (e.g.
// "https://acme.slack.com") is required for browser auth and optional for
// standard tokens.
//
// Browser credentials (xoxc token + "d" cookie) are sent to the workspace
// URL itself, so the client refuses to call the API when that URL is not an
// https Slack workspace origin (see WorkspaceOrigin): the call fails before
// anything is sent. Standard tokens always go to https://slack.com.
func New(auth Auth, workspaceURL string, opts ...Option) *Client {
	c := &Client{
		auth:         auth,
		workspaceURL: strings.TrimRight(workspaceURL, "/"),
		baseURL:      defaultBaseURL,
		httpClient:   &http.Client{},
		sleep:        sleepCtx,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewFake returns a browser-auth client for workspace
// https://test.slack.com whose API calls are served by fn.
func NewFake(fn Handler, opts ...Option) *Client {
	auth := Auth{Type: AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"}
	return New(auth, "https://test.slack.com", append([]Option{WithHandler(fn)}, opts...)...)
}

// Auth returns the client's credentials.
func (c *Client) Auth() Auth { return c.auth }

// WorkspaceURL returns the workspace origin, without trailing slash ("" if
// unknown).
func (c *Client) WorkspaceURL() string { return c.workspaceURL }

// HTTPClient returns the underlying HTTP client, for non-API requests such
// as file downloads and uploads.
func (c *Client) HTTPClient() *http.Client { return c.httpClient }

// Authorize adds credentials and the User-Agent to a non-API request (file
// download/upload). Only call it for requests to Slack-owned hosts: as a
// last line of defence, a request that is not https to a Slack host
// (IsSlackHost) only gets the User-Agent, never the token or the cookie.
func (c *Client) Authorize(req *http.Request) {
	req.Header.Set("User-Agent", appinfo.UserAgent())
	if !c.mayReceiveCredentials(req.URL) {
		return
	}
	if t := c.auth.BearerToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	if c.auth.IsBrowser() {
		req.Header.Set("Cookie", "d="+EncodeCookieD(c.auth.XoxdCookie))
	}
}

// APITimeout is the per-request timeout (SLACKCLI_API_TIMEOUT_MS, legacy
// SLACK_API_TIMEOUT_MS; default 20s).
func APITimeout() time.Duration {
	return appinfo.EnvDurationMS("API_TIMEOUT_MS", legacyAPITimeout(), false)
}

func legacyAPITimeout() time.Duration {
	n, err := strconv.ParseFloat(strings.TrimSpace(getenv("SLACK_API_TIMEOUT_MS")), 64)
	if err != nil || n <= 0 {
		return defaultAPITimeout
	}
	return time.Duration(n) * time.Millisecond
}

// RateLimitMaxWait is how long the client is willing to sleep on HTTP 429
// before retrying (SLACKCLI_RATE_LIMIT_MAX_WAIT_MS; default 0 = fail fast).
func RateLimitMaxWait() time.Duration {
	return appinfo.EnvDurationMS("RATE_LIMIT_MAX_WAIT_MS", 0, true)
}

// API calls a Web API method with form-encoded parameters. Nil parameters
// are dropped; strings are sent as-is; bools and numbers are stringified;
// everything else (maps, slices, structs) is JSON-encoded.
//
// A response with ok=false is returned as an *APIError.
func (c *Client) API(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	return c.call(ctx, method, params, false)
}

// APIMultipart is like API but uses multipart/form-data. Some internal
// Slack methods (e.g. saved.update) silently ignore form-encoded parameters.
func (c *Client) APIMultipart(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	return c.call(ctx, method, params, true)
}

func (c *Client) call(ctx context.Context, method string, params map[string]any, multi bool) (map[string]any, error) {
	if c.handler != nil {
		resp, err := c.handler(ctx, method, params)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			resp = map[string]any{"ok": true}
		}
		if ok, present := resp["ok"]; present && ok != true {
			return nil, newAPIError(method, resp)
		}
		return resp, nil
	}

	endpoint, err := c.endpoint(method)
	if err != nil {
		return nil, err
	}
	fields, err := EncodeParams(params)
	if err != nil {
		return nil, fmt.Errorf("encoding parameters for %s: %w", method, err)
	}
	if c.auth.IsBrowser() {
		fields["token"] = c.auth.XoxcToken
	}

	timeout := APITimeout()
	for attempt := 0; ; attempt++ {
		status, header, body, err := c.post(ctx, endpoint, fields, multi, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ne interface{ Timeout() bool }
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
				return nil, timeoutError(method, timeout)
			}
			return nil, fmt.Errorf("calling Slack API %s: %w", method, err)
		}

		if status == http.StatusTooManyRequests {
			retryAfter := parseRetryAfter(header.Get("Retry-After"))
			maxWait := RateLimitMaxWait()
			if attempt >= maxRateLimitRetry || retryAfter > maxWait {
				return nil, &RateLimitError{Method: method, RetryAfter: retryAfter, MaxWait: maxWait}
			}
			if err := c.sleep(ctx, retryAfter); err != nil {
				return nil, err
			}
			continue
		}

		var data map[string]any
		_ = json.Unmarshal(body, &data)
		if status < 200 || status > 299 {
			if code, _ := data["error"].(string); code != "" {
				return nil, newAPIError(method, data)
			}
			return nil, fmt.Errorf("Slack HTTP %d calling %s", status, method)
		}
		if data == nil {
			return nil, fmt.Errorf("Slack API %s returned a non-JSON response", method)
		}
		if data["ok"] != true {
			return nil, newAPIError(method, data)
		}
		return data, nil
	}
}

func (c *Client) endpoint(method string) (string, error) {
	if c.auth.IsBrowser() {
		if c.workspaceURL == "" {
			return "", errors.New("browser auth (xoxc token) requires a workspace URL: pass --workspace, set SLACK_WORKSPACE_URL, or use a Slack message URL as the target")
		}
		if err := c.checkWorkspaceURL(); err != nil {
			return "", err
		}
		return c.workspaceURL + "/api/" + method, nil
	}
	if c.auth.Token == "" {
		return "", errors.New("missing Slack token")
	}
	return c.baseURL + "/api/" + method, nil
}

// checkWorkspaceURL refuses a workspace URL that browser credentials must
// not be sent to. It is the client's own guard: callers are expected to
// have validated the URL already (auth.NormalizeURL).
func (c *Client) checkWorkspaceURL() error {
	if origin, err := WorkspaceOrigin(c.workspaceURL); err == nil && origin == strings.ToLower(c.workspaceURL) {
		return nil
	}
	if u, err := url.Parse(c.workspaceURL); err == nil && c.loopbackForTesting && isLoopbackURL(u) {
		return nil
	}
	return fmt.Errorf("refusing to send browser credentials to %q: a workspace URL must be an https Slack origin such as https://acme.slack.com", c.workspaceURL)
}

// mayReceiveCredentials reports whether a non-API request to u may carry
// the token and the cookie.
func (c *Client) mayReceiveCredentials(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Scheme == "https" && u.User == nil && IsSlackHost(u.Hostname()) {
		return true
	}
	return c.loopbackForTesting && isLoopbackURL(u)
}

// readOnlyVerbs are the last segments of API method names that only read
// ("conversations.history", "users.info"…).
var readOnlyVerbs = map[string]bool{
	"list": true, "info": true, "history": true, "replies": true, "test": true,
	"get": true, "lookup": true, "counts": true, "members": true, "search": true,
}

// isReadOnlyMethod reports whether an API method obviously changes nothing,
// judging by its name. When in doubt it answers false.
func isReadOnlyMethod(method string) bool {
	if strings.HasPrefix(method, "search.") {
		return true
	}
	return readOnlyVerbs[method[strings.LastIndex(method, ".")+1:]]
}

// timeoutError is returned when Slack did not answer in time. A request
// that may have changed something says so: the caller must not blindly
// retry it.
func timeoutError(method string, timeout time.Duration) error {
	note := ""
	if !isReadOnlyMethod(method) {
		note = "; the request may still have been processed, so check before retrying"
	}
	return fmt.Errorf("Slack API call %s timed out after %s%s. Set %s to adjust.",
		method, timeout, note, appinfo.EnvName("API_TIMEOUT_MS"))
}

func (c *Client) post(ctx context.Context, endpoint string, fields map[string]string, multi bool, timeout time.Duration) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	var contentType string
	if multi {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, k := range sortedKeys(fields) {
			if err := w.WriteField(k, fields[k]); err != nil {
				return 0, nil, nil, err
			}
		}
		if err := w.Close(); err != nil {
			return 0, nil, nil, err
		}
		body, contentType = &buf, w.FormDataContentType()
	} else {
		form := url.Values{}
		for k, v := range fields {
			form.Set(k, v)
		}
		body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", appinfo.UserAgent())
	if c.auth.IsBrowser() {
		req.Header.Set("Cookie", "d="+EncodeCookieD(c.auth.XoxdCookie))
		req.Header.Set("Origin", "https://app.slack.com")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.auth.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyLen))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, data, nil
}

// EncodeParams converts API parameters to form fields (see Client.API).
func EncodeParams(params map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(params)+1)
	for k, v := range params {
		s, ok, err := encodeParam(v)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", k, err)
		}
		if ok {
			out[k] = s
		}
	}
	return out, nil
}

func encodeParam(v any) (string, bool, error) {
	switch x := v.(type) {
	case nil:
		return "", false, nil
	case string:
		return x, true, nil
	case bool:
		return strconv.FormatBool(x), true, nil
	case int:
		return strconv.Itoa(x), true, nil
	case int64:
		return strconv.FormatInt(x, 10), true, nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true, nil
	case json.Number:
		return x.String(), true, nil
	case fmt.Stringer:
		return x.String(), true, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", false, err
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	if s == "null" {
		return "", false, nil
	}
	return s, true, nil
}

// EncodeCookieD percent-encodes a decoded xoxd cookie value for use in a
// Cookie header (JavaScript's encodeURIComponent).
func EncodeCookieD(v string) string {
	return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
}

// DecodeCookieD normalises a "d" cookie value to its decoded form. Values
// copied from a browser are percent-encoded ("xoxd-abc%2Bdef"); values that
// are already decoded (or not valid percent-encoding) are returned as-is.
func DecodeCookieD(v string) string {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "%") {
		return v
	}
	if d, err := url.PathUnescape(v); err == nil {
		return d
	}
	return v
}

func parseRetryAfter(h string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(h), 64)
	if err != nil || secs < 1 {
		if err != nil {
			secs = 5
		} else {
			secs = 1
		}
	}
	d := time.Duration(secs * float64(time.Second))
	if d > maxRateLimitDelay {
		d = maxRateLimitDelay
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
