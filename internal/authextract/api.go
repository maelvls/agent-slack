// Package authextract extracts Slack browser-session credentials (xoxc
// tokens + the xoxd "d" cookie) from local applications: Slack Desktop,
// Chrome, Brave and Firefox, plus pasted "Copy as cURL" commands.
//
// This file is the contract the rest of the program relies on; the
// extractors themselves live in the other files of the package.
package authextract

import "errors"

// Team is one signed-in workspace found by an extractor.
type Team struct {
	// URL is the workspace URL, e.g. "https://acme.slack.com/".
	URL string
	// Name is the workspace display name, if known.
	Name string
	// Token is the xoxc- token.
	Token string
}

// Extracted is the result of a successful extraction: at least one team and
// a non-empty, URL-decoded cookie.
type Extracted struct {
	// CookieD is the decoded xoxd- cookie shared by Teams.
	CookieD string
	Teams   []Team
	// Source describes where the credentials came from, for messages
	// (e.g. "Slack Desktop", "Firefox profile default-release").
	Source string
}

// ErrBraveAppleScriptDisabled is returned by Brave when "Allow JavaScript
// from Apple Events" is turned off in Brave.
var ErrBraveAppleScriptDisabled = errors.New(
	"Brave has JavaScript from Apple Events disabled: enable View → Developer → Allow JavaScript from Apple Events, then retry")

// ErrNotFound is returned (possibly wrapped) by an extractor when the
// application is not installed, not running, or holds no Slack session. The
// automatic credential lookup treats it as "try the next source".
var ErrNotFound = errors.New("no Slack session found")
