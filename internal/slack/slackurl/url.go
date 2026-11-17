// Package slackurl parses and builds Slack message permalinks, and
// classifies the "target" argument most commands take (message URL,
// channel, or user).
package slackurl

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackid"
)

// MessageRef identifies one message, as parsed from a permalink.
type MessageRef struct {
	// WorkspaceURL is the workspace origin, e.g. "https://acme.slack.com".
	// It is "" for app.slack.com URLs, which only carry a team ID: callers
	// then fall back to --workspace / the default workspace.
	WorkspaceURL string `json:"workspace_url"`
	// TeamID is the T…/E… ID of app.slack.com/client/<team>/… URLs ("" for
	// ordinary permalinks).
	TeamID    string `json:"team_id,omitempty"`
	ChannelID string `json:"channel_id"`
	// MessageTS is the message timestamp, "1234567890.123456".
	MessageTS string `json:"message_ts"`
	// ThreadTSHint is the thread root ts taken from ?thread_ts=…: set when
	// the permalink points at a thread reply.
	ThreadTSHint string `json:"thread_ts_hint,omitempty"`
	// Raw is the URL as given.
	Raw string `json:"raw"`
	// PossiblyTruncated is true when the URL has thread_ts but no cid,
	// which usually means an unquoted "&" was eaten by the shell.
	PossiblyTruncated bool `json:"possibly_truncated,omitempty"`
}

var (
	tsRe          = regexp.MustCompile(`^\d{6,}\.\d{6}$`)
	permalinkIDRe = regexp.MustCompile(`^p(\d{7,})$`)
	teamIDRe      = regexp.MustCompile(`^[TE][A-Z0-9]{2,}$`)
)

// IsTS reports whether s is a full Slack timestamp ("1770165109.628379").
func IsTS(s string) bool { return tsRe.MatchString(s) }

// IsSlackURL reports whether input is an https URL on a Slack workspace
// host (*.slack.com, *.slack-gov.com), whatever its path.
func IsSlackURL(input string) bool {
	u, err := url.Parse(strings.TrimSpace(input))
	return err == nil && u.Scheme == "https" && isWorkspaceHost(u.Hostname())
}

func isWorkspaceHost(host string) bool {
	host = strings.ToLower(host)
	return strings.HasSuffix(host, ".slack.com") || strings.HasSuffix(host, ".slack-gov.com")
}

// location is what a Slack URL points at: a channel, or a message in it.
type location struct {
	workspaceURL string
	teamID       string
	channelID    string
	messageTS    string // "" for a channel URL
	threadTS     string
	truncated    bool
}

func parseLocation(input string) (location, error) {
	var loc location
	u, err := url.Parse(input)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return loc, fmt.Errorf("invalid URL: %s", input)
	}
	host := strings.ToLower(u.Hostname())
	if !isWorkspaceHost(host) {
		return loc, fmt.Errorf("not a Slack workspace URL: %s", u.Hostname())
	}
	// The workspace URL is where credentials get sent, so plain http and
	// "user@host" tricks are refused rather than normalised.
	if u.Scheme != "https" || u.User != nil {
		return loc, fmt.Errorf("unsupported Slack URL (expected https://<workspace>.slack.com/…): %s", input)
	}

	var parts []string
	for p := range strings.SplitSeq(u.Path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}

	switch {
	case host == "app.slack.com":
		// /client/<team>/<channel>[/thread/<channel>-<ts> | /p<digits> | /<ts>]
		if len(parts) < 3 || parts[0] != "client" || !teamIDRe.MatchString(parts[1]) {
			return loc, fmt.Errorf("unsupported Slack URL path: %s", u.Path)
		}
		loc.teamID, loc.channelID = parts[1], parts[2]
		switch {
		case len(parts) >= 5 && parts[3] == "thread":
			ch, ts, ok := strings.Cut(parts[4], "-")
			if !ok || ch != loc.channelID || !IsTS(ts) {
				return loc, fmt.Errorf("unsupported Slack thread id: %s", parts[4])
			}
			loc.messageTS, loc.threadTS = ts, ts
		case len(parts) >= 4 && IsTS(parts[3]):
			loc.messageTS = parts[3]
		case len(parts) >= 4:
			ts, err := permalinkTS(parts[3])
			if err != nil {
				return loc, err
			}
			loc.messageTS = ts
		}
	case len(parts) >= 2 && parts[0] == "archives":
		// /archives/<channel>[/p<digits>]
		loc.workspaceURL = "https://" + strings.ToLower(u.Host)
		loc.channelID = parts[1]
		if len(parts) >= 3 {
			ts, err := permalinkTS(parts[2])
			if err != nil {
				return loc, err
			}
			loc.messageTS = ts
		}
	default:
		return loc, fmt.Errorf("unsupported Slack URL path: %s", u.Path)
	}

	if !slackid.IsChannelID(loc.channelID) {
		return loc, fmt.Errorf("unsupported Slack channel id in URL: %s", loc.channelID)
	}
	query := u.Query()
	if t := query.Get("thread_ts"); t != "" {
		if IsTS(t) {
			loc.threadTS = t
		}
		// Slack always puts cid next to thread_ts; a lone thread_ts means
		// the shell cut the URL at "&".
		loc.truncated = !query.Has("cid")
	}
	return loc, nil
}

func permalinkTS(part string) (string, error) {
	m := permalinkIDRe.FindStringSubmatch(part)
	if m == nil {
		return "", fmt.Errorf("unsupported Slack message id: %s", part)
	}
	digits := m[1]
	return digits[:len(digits)-6] + "." + digits[len(digits)-6:], nil
}

// ParseMessageURL parses a message permalink:
//
//	https://acme.slack.com/archives/C…/p1770165109628379[?thread_ts=…&cid=…]
//	https://acme.enterprise.slack.com/archives/C…/p…
//	https://app.slack.com/client/T…/C…/thread/C…-1770165109.628379
//
// It fails for anything else, including URLs that point at a channel
// rather than a message.
func ParseMessageURL(input string) (MessageRef, error) {
	loc, err := parseLocation(strings.TrimSpace(input))
	if err != nil {
		return MessageRef{}, err
	}
	if loc.messageTS == "" {
		return MessageRef{}, fmt.Errorf("Slack URL points at channel %s, not at a message: copy a message link, or pass the channel ID with a message ts", loc.channelID)
	}
	return MessageRef{
		WorkspaceURL:      loc.workspaceURL,
		TeamID:            loc.teamID,
		ChannelID:         loc.channelID,
		MessageTS:         loc.messageTS,
		ThreadTSHint:      loc.threadTS,
		Raw:               input,
		PossiblyTruncated: loc.truncated,
	}, nil
}

// BuildMessageURL builds the permalink of a message. threadTS is the thread
// root ts; pass "" (or messageTS itself) for a top-level message.
func BuildMessageURL(workspaceURL, channelID, messageTS, threadTS string) string {
	link := strings.TrimRight(workspaceURL, "/") + "/archives/" + url.PathEscape(channelID) + "/p" + permalinkDigits(messageTS)
	if threadTS != "" && threadTS != messageTS {
		link += "?thread_ts=" + url.QueryEscape(threadTS) + "&cid=" + url.QueryEscape(channelID)
	}
	return link
}

// permalinkDigits turns "1770165109.628379" into "1770165109628379". The
// fraction is padded to six digits: a ts that lost its trailing zeros (for
// instance after a round-trip through a number) must still give the right
// permalink.
func permalinkDigits(ts string) string {
	secs, frac, _ := strings.Cut(ts, ".")
	if len(frac) > 6 {
		frac = frac[:6]
	}
	return secs + frac + strings.Repeat("0", 6-len(frac))
}

// TruncatedURLHint returns the hint to show on stderr (rt.Hintf) when ref
// looks like it was cut by the shell at an unquoted "&"; "" when it does
// not.
func TruncatedURLHint(ref MessageRef) string {
	if !ref.PossiblyTruncated {
		return ""
	}
	return `Hint: URL may have been truncated by shell. Quote URLs containing "&":` + "\n" +
		`  ` + appinfo.Name + ` message get "https://...?thread_ts=...&cid=..."`
}
