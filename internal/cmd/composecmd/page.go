package composecmd

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// editorHTML is the editor page. It holds two placeholders, filled in by
// renderPage: the CSP nonce of its one script, and the JSON configuration.
//
//go:embed editor.html
var editorHTML string

const (
	noncePlaceholder  = "__CSP_NONCE__"
	configPlaceholder = "__COMPOSE_CONFIG__"
)

// pageConfig is what the editor page is told about the session. All of it
// is displayed as text or parsed by the page's own mrkdwn renderer, which
// escapes it: channel names and the initial text are untrusted.
type pageConfig struct {
	AppName       string `json:"appName"`
	ChannelName   string `json:"channelName"`
	IsDM          bool   `json:"isDM"`
	WorkspaceName string `json:"workspaceName"`
	ThreadTS      string `json:"threadTs"`
	// ThreadURL is the permalink of the thread being replied to ("" when
	// unknown).
	ThreadURL   string `json:"threadUrl"`
	InitialText string `json:"initialText"`
	// Token must accompany every POST of the page.
	Token string `json:"token"`
	// PingMillis is how often the page says it is still open.
	PingMillis int64 `json:"pingMillis"`
}

// renderPage returns the editor page for cfg, with nonce as the nonce of
// its script.
//
// The configuration is embedded as a JSON data block (a <script
// type="application/json">, which browsers never execute) rather than as
// JavaScript source. encoding/json escapes <, > and & as <, > and
// & (and U+2028/U+2029), so no value can close the block or open a
// comment or another element.
func renderPage(cfg pageConfig, nonce string) (string, error) {
	blob, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	// The configuration goes in last, and once: text that happens to
	// contain a placeholder is not substituted.
	page := strings.ReplaceAll(editorHTML, noncePlaceholder, nonce)
	return strings.Replace(page, configPlaceholder, string(blob), 1), nil
}

// contentSecurityPolicy lets the page run its one nonce-carrying script
// and talk to this server, and nothing else: no inline event handlers (so
// markup injected through the message text cannot run), no remote
// resources, no framing, no form posts. Inline styles are allowed: the
// editor relies on style attributes, and without script or remote loads
// they cannot exfiltrate anything.
func contentSecurityPolicy(nonce string) string {
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'nonce-" + nonce + "'",
		"style-src 'unsafe-inline'",
		"img-src data:",
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}
