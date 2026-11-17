package slackurl

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/slackid"
)

// TargetKind says what a Target designates.
type TargetKind string

const (
	// TargetURL is a message permalink.
	TargetURL TargetKind = "url"
	// TargetChannel is a channel, by "#name" or by ID.
	TargetChannel TargetKind = "channel"
	// TargetUser is a user ID, i.e. the DM with that user.
	TargetUser TargetKind = "user"
)

// Target is a parsed <target> argument.
type Target struct {
	Kind TargetKind
	// Ref is set for TargetURL.
	Ref MessageRef
	// Channel is set for TargetChannel: a conversation ID, or a name with
	// its leading "#" (resolve it with channels.ResolveID).
	Channel string
	// UserID is set for TargetUser.
	UserID string
	// WorkspaceURL is the workspace the target itself names: Ref's for
	// TargetURL, the URL's for a channel given as a Slack channel URL, ""
	// otherwise (use --workspace / the default workspace).
	WorkspaceURL string
}

// ParseTarget classifies a <target> argument: a Slack message URL, a user
// ID (U…/W…, meaning the DM with that user), a channel ID, "#name" or a
// bare channel name. A Slack URL that points at a channel rather than a
// message is a channel target.
func ParseTarget(input string) (Target, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return Target{}, errors.New("missing target: pass a Slack message URL, a channel (#name or ID) or a user ID")
	}

	if strings.Contains(trimmed, "://") {
		loc, err := parseLocation(trimmed)
		if err != nil {
			// A URL is never a channel name: say why it was rejected
			// instead of looking up a channel called "#https://…".
			return Target{}, fmt.Errorf("unsupported target URL (expected a Slack message link such as https://<workspace>.slack.com/archives/C…/p…): %w", err)
		}
		if loc.messageTS == "" {
			return Target{Kind: TargetChannel, Channel: loc.channelID, WorkspaceURL: loc.workspaceURL}, nil
		}
		ref, err := ParseMessageURL(trimmed)
		if err != nil {
			return Target{}, err
		}
		return Target{Kind: TargetURL, Ref: ref, WorkspaceURL: ref.WorkspaceURL}, nil
	}

	if strings.Contains(trimmed, "/") {
		// No channel name contains a slash: this is a URL without its
		// scheme, or a path.
		return Target{}, fmt.Errorf("unsupported target %q: pass a full Slack message URL (https://…), a channel (#name or ID) or a user ID", trimmed)
	}

	switch {
	case slackid.IsUserID(trimmed):
		return Target{Kind: TargetUser, UserID: trimmed}, nil
	case strings.HasPrefix(trimmed, "#"), slackid.IsChannelID(trimmed):
		return Target{Kind: TargetChannel, Channel: trimmed}, nil
	}
	// Bare channel names ("general") are allowed for convenience.
	return Target{Kind: TargetChannel, Channel: "#" + trimmed}, nil
}
