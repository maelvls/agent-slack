package slackapi

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

func getenv(k string) string { return os.Getenv(k) }

// APIError is a Slack Web API response with ok=false.
type APIError struct {
	// Method is the API method that failed, e.g. "conversations.history".
	Method string
	// Code is Slack's error code, e.g. "channel_not_found".
	Code string
	// Response is the full decoded response body.
	Response map[string]any
}

func newAPIError(method string, resp map[string]any) *APIError {
	code, _ := resp["error"].(string)
	return &APIError{Method: method, Code: code, Response: resp}
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return "Slack API error calling " + e.Method
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Slack API error calling %s: %s", e.Method, e.Code)
	// missing_scope & co. come with the scopes involved; they are what the
	// user needs to fix the problem.
	if needed, _ := e.Response["needed"].(string); needed != "" {
		fmt.Fprintf(&b, " (needed: %s", needed)
		if provided, _ := e.Response["provided"].(string); provided != "" {
			fmt.Fprintf(&b, "; provided: %s", provided)
		}
		b.WriteString(")")
	}
	if md, _ := e.Response["response_metadata"].(map[string]any); md != nil {
		if msgs, _ := md["messages"].([]any); len(msgs) > 0 {
			parts := make([]string, 0, len(msgs))
			for _, m := range msgs {
				if s, _ := m.(string); s != "" {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				fmt.Fprintf(&b, " [%s]", strings.Join(parts, "; "))
			}
		}
	}
	return b.String()
}

// ErrorCode returns the Slack error code carried by err ("" if err is not,
// and does not wrap, an *APIError).
func ErrorCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// IsErrorCode reports whether err carries one of the given Slack error
// codes.
func IsErrorCode(err error, codes ...string) bool {
	code := ErrorCode(err)
	if code == "" {
		return false
	}
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

// IsAuthError reports whether err means the credentials are no longer
// valid, i.e. re-importing them may help.
func IsAuthError(err error) bool {
	return IsErrorCode(err, "invalid_auth", "token_expired", "not_authed", "account_inactive", "token_revoked")
}

// RateLimitError is returned on HTTP 429 when the wait Slack asks for
// exceeds what the caller allows.
type RateLimitError struct {
	Method     string
	RetryAfter time.Duration
	MaxWait    time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("Slack API call %s was rate limited; retry after %s (set %s to wait and retry automatically; currently %dms).",
		e.Method, e.RetryAfter, appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS"), e.MaxWait.Milliseconds())
}
