// Package canvas creates, edits and reads Slack canvases as Markdown.
package canvas

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// Ref is what a canvas URL points at.
type Ref struct {
	// WorkspaceURL is the workspace origin ("https://acme.slack.com"). It
	// is "" for app.slack.com URLs, which do not name their workspace.
	WorkspaceURL string
	// CanvasID is the canvas's file ID (F…); "" for a channel canvas URL.
	CanvasID string
	// ChannelID is set instead of CanvasID for a channel canvas URL
	// (/canvas/C…); see ChannelCanvasID.
	ChannelID string
}

var idRe = regexp.MustCompile(`^F[A-Z0-9]{8,}$`)

// IsID reports whether s looks like a canvas ID, which is a file ID (F…).
func IsID(s string) bool { return idRe.MatchString(s) }

// ParseURL parses a canvas URL:
//
//	https://acme.slack.com/docs/T…/F…   a canvas
//	https://acme.slack.com/canvas/C…    the canvas of a channel
//
// The workspace is where credentials get sent, so anything that is not an
// https URL on a Slack workspace host is refused.
func ParseURL(input string) (Ref, error) {
	raw := strings.TrimSpace(input)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Ref{}, fmt.Errorf("invalid canvas URL %q", input)
	}
	if !slackurl.IsSlackURL(raw) || u.User != nil {
		return Ref{}, fmt.Errorf("not a Slack workspace URL: %q (expected https://<workspace>.slack.com/docs/T…/F…)", input)
	}

	ref := Ref{WorkspaceURL: "https://" + strings.ToLower(u.Host)}
	if strings.EqualFold(u.Hostname(), "app.slack.com") {
		ref.WorkspaceURL = ""
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	switch {
	case len(parts) >= 2 && parts[0] == "docs":
		for _, p := range parts[1:] {
			if IsID(p) {
				ref.CanvasID = p
				return ref, nil
			}
		}
		return Ref{}, fmt.Errorf("no canvas ID (F…) in canvas URL %q", input)
	case len(parts) >= 2 && parts[0] == "canvas" && slackid.IsChannelID(parts[1]):
		ref.ChannelID = parts[1]
		return ref, nil
	}
	return Ref{}, fmt.Errorf("not a canvas URL: %q (expected https://<workspace>.slack.com/docs/T…/F… or …/canvas/C…)", input)
}

// ChannelCanvasID returns the ID of a channel's canvas, the one a
// /canvas/C… URL points at (conversations.info).
func ChannelCanvasID(ctx context.Context, client *slackapi.Client, channelID string) (string, error) {
	resp, err := client.API(ctx, "conversations.info", map[string]any{"channel": channelID})
	if err != nil {
		return "", fmt.Errorf("looking up the canvas of channel %s: %w", channelID, err)
	}
	id := jsonx.GetStr(resp, "channel", "properties", "canvas", "file_id")
	if id == "" {
		return "", fmt.Errorf("channel %s has no channel canvas", channelID)
	}
	return id, nil
}
