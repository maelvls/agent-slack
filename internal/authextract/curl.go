package authextract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/slackapi"
)

var (
	curlURLPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s'"]+`)
	// The "d" cookie, wherever the command carries it: -b/--cookie, a
	// Cookie header, in any quoting style.
	curlCookiePattern = regexp.MustCompile(`(?:^|[\s;'":])d=(xoxd-[^\s;'"\\]+)`)
	curlTokenPattern  = regexp.MustCompile(`xoxc-[A-Za-z0-9-]+`)
)

// nonWorkspaceHosts are Slack hosts that serve every workspace: a request
// to one of them does not say which workspace the credentials belong to.
var nonWorkspaceHosts = map[string]bool{
	"slack.com":         true,
	"www.slack.com":     true,
	"app.slack.com":     true,
	"api.slack.com":     true,
	"edgeapi.slack.com": true,
	"files.slack.com":   true,
}

// ParseCurl extracts browser credentials from a Slack API request copied
// from the browser's developer tools with "Copy as cURL" (bash or Windows
// cmd flavour). The request must be one sent to the workspace's own host,
// e.g. https://acme.slack.com/api/client.counts.
func ParseCurl(input string) (*Extracted, error) {
	// "Copy as cURL (cmd)" escapes quotes and percent signs with carets.
	if strings.Contains(input, `^"`) {
		input = strings.ReplaceAll(input, "^", "")
	}

	// In every flavour the first URL of the command is the request URL.
	requestURL := curlURLPattern.FindString(input)
	if requestURL == "" {
		return nil, errors.New("could not find the request URL in the cURL command: paste a Slack API request copied with \"Copy as cURL\"")
	}
	origin := slackOrigin(requestURL)
	if origin == "" {
		return nil, fmt.Errorf("the cURL command is not an https request to Slack (found %q): copy a request to https://<workspace>.slack.com/api/…", truncateForError(requestURL))
	}
	if host := strings.TrimPrefix(origin, "https://"); nonWorkspaceHosts[host] {
		return nil, fmt.Errorf("the cURL command is a request to %s, which does not identify a workspace: copy a request to https://<workspace>.slack.com/api/… instead (filter the Network tab on \"/api/\")", host)
	}

	cookie := curlCookiePattern.FindStringSubmatch(input)
	if cookie == nil {
		return nil, errors.New("could not find the xoxd cookie (d=xoxd-…) in the cURL command: make sure the copied request includes its Cookie header")
	}
	token := curlTokenPattern.FindString(input)
	if token == "" {
		return nil, errors.New("could not find an xoxc token in the cURL command: copy a POST request to /api/… whose form data includes the token")
	}
	return &Extracted{
		CookieD: slackapi.DecodeCookieD(cookie[1]),
		Teams:   []Team{{URL: origin, Token: token}},
		Source:  "cURL command",
	}, nil
}

// truncateForError keeps error messages short and free of the query
// string, which may carry a token.
func truncateForError(rawURL string) string {
	if i := strings.IndexAny(rawURL, "?#"); i >= 0 {
		rawURL = rawURL[:i]
	}
	if len(rawURL) > 80 {
		rawURL = rawURL[:80] + "…"
	}
	return rawURL
}
