// Package cmdutil holds what the command packages share: turning a
// <target> argument and the --workspace/--ts flags into a workspace, a
// conversation and a message; flag validation; and free-text arguments
// that start with a dash.
package cmdutil

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// Workspace picks the workspace selector to hand to clictx.Context.Client.
// In order: the workspace a URL argument names (named, "" if it names
// none), the --workspace flag, SLACK_WORKSPACE_URL, and "" for the default
// workspace.
//
// A URL always wins, because it is unambiguous. A --workspace flag given
// along with it may repeat it but not contradict it: silently acting in a
// workspace other than the one asked for is worse than an error.
// SLACK_WORKSPACE_URL is only an ambient default, so a URL naming another
// workspace overrides it without complaint.
func Workspace(rt *clictx.Context, named, flag string) (string, error) {
	if named == "" {
		return rt.EffectiveWorkspace(flag), nil
	}
	if flag = strings.TrimSpace(flag); flag != "" && !selects(rt, flag, named) {
		return "", fmt.Errorf("--workspace %q contradicts the URL, which is in workspace %s: drop --workspace (a URL selects its own workspace)", flag, named)
	}
	return named, nil
}

// selects reports whether a --workspace selector designates workspaceURL.
func selects(rt *clictx.Context, selector, workspaceURL string) bool {
	if u, err := auth.NormalizeURL(workspaceURL); err == nil {
		workspaceURL = u
	}
	if u, err := auth.NormalizeURL(selector); err == nil {
		return u == workspaceURL
	}
	if creds, err := rt.Store.Load(); err == nil {
		if match, _ := auth.SelectWorkspace(creds.Workspaces, selector); match != nil {
			return match.URL == workspaceURL
		}
	}
	// Not a configured workspace (SLACK_TOKEN, or not imported yet).
	return auth.MatchesSelector(workspaceURL, "", selector)
}

// Target is a parsed <target> argument together with the workspace it is
// to be looked up in.
type Target struct {
	slackurl.Target
	// Workspace is the selector to pass to clictx.Context.Client ("" for
	// the default workspace).
	Workspace string
}

// ParseTarget parses a <target> argument (a Slack message or channel URL,
// "#channel", "channel", a channel ID, or a user ID meaning the DM with
// that user) and picks its workspace (see Workspace). It makes no API
// call. It fails when a channel name cannot be attributed to a workspace,
// and warns on stderr about a message URL that looks cut at an unquoted
// "&".
func ParseTarget(rt *clictx.Context, arg, workspaceFlag string) (Target, error) {
	parsed, err := slackurl.ParseTarget(arg)
	if err != nil {
		return Target{}, err
	}
	workspace, err := Workspace(rt, parsed.WorkspaceURL, workspaceFlag)
	if err != nil {
		return Target{}, err
	}
	switch parsed.Kind {
	case slackurl.TargetChannel:
		if err := rt.AssertWorkspaceForChannelNames(workspace, parsed.Channel); err != nil {
			return Target{}, err
		}
	case slackurl.TargetURL:
		if hint := slackurl.TruncatedURLHint(parsed.Ref); hint != "" {
			rt.Hintf("%s", hint)
		}
	}
	return Target{Target: parsed, Workspace: workspace}, nil
}

// ParseChannel is ParseTarget for an argument or flag that must designate
// a channel: an ID, "#name", "name" or a channel URL.
func ParseChannel(rt *clictx.Context, arg, workspaceFlag string) (Target, error) {
	if strings.Trim(strings.TrimSpace(arg), "#") == "" {
		return Target{}, errors.New("the channel is empty: pass a channel ID (C…) or name (#channel)")
	}
	t, err := ParseTarget(rt, arg, workspaceFlag)
	if err != nil {
		return Target{}, err
	}
	if t.Kind != slackurl.TargetChannel {
		return Target{}, fmt.Errorf("%q is not a channel: pass a channel ID (C…) or name (#channel)", strings.TrimSpace(arg))
	}
	return t, nil
}

// ParseMessageTarget is ParseTarget for a command that acts on one
// message: <target> is a message URL, or a channel together with --ts. It
// returns the ts of that message as well (see Target.NoUser and
// Target.MessageTS). command names the command in errors.
func ParseMessageTarget(rt *clictx.Context, command, arg, workspaceFlag, tsFlag string) (Target, string, error) {
	t, err := ParseTarget(rt, arg, workspaceFlag)
	if err != nil {
		return Target{}, "", err
	}
	if err := t.NoUser(command); err != nil {
		return Target{}, "", err
	}
	ts, err := t.MessageTS(tsFlag)
	if err != nil {
		return Target{}, "", err
	}
	return t, ts, nil
}

// NoUser rejects a user-ID target, for commands that designate a message
// or a channel rather than somewhere to write to. command names the
// command in the error.
func (t Target) NoUser(command string) error {
	if t.Kind != slackurl.TargetUser {
		return nil
	}
	return fmt.Errorf(`%q does not support user ID targets: pass a Slack message URL, or the DM's channel ID (D…, see "%s user dm-open %s") with --ts`, command, appinfo.Name, t.UserID)
}

// MessageTS returns the ts of the message the target designates: the one
// in a message URL, else the --ts flag, which a channel target requires.
// --ts may repeat the ts of a URL but not contradict it.
func (t Target) MessageTS(tsFlag string) (string, error) {
	ts, err := ParseTS("--ts", tsFlag)
	if err != nil {
		return "", err
	}
	if t.Kind == slackurl.TargetURL {
		if ts != "" && ts != t.Ref.MessageTS {
			return "", fmt.Errorf("--ts %s contradicts the message URL, which points at %s: drop --ts, or pass the channel instead of the URL", ts, t.Ref.MessageTS)
		}
		return t.Ref.MessageTS, nil
	}
	if ts == "" {
		return "", errors.New(`--ts is required when the target is a channel: pass --ts "<seconds>.<micros>" (e.g. 1770165109.628379), or a Slack message URL as the target`)
	}
	return ts, nil
}

// ConversationID returns the ID of the conversation the target designates:
// the channel of a URL, the channel a name resolves to, or the DM with a
// user (opened if need be).
func (t Target) ConversationID(ctx context.Context, client *slackapi.Client) (string, error) {
	switch t.Kind {
	case slackurl.TargetURL:
		return t.Ref.ChannelID, nil
	case slackurl.TargetUser:
		return channels.OpenDM(ctx, client, t.UserID)
	}
	return channels.ResolveID(ctx, client, t.Channel)
}

// ReplyThreadTS returns the thread a message written to a message-URL
// target goes to: the thread the linked message belongs to, or the one it
// starts. It is "" for the other kinds of target.
func (t Target) ReplyThreadTS(ctx context.Context, client *slackapi.Client) (string, error) {
	if t.Kind != slackurl.TargetURL {
		return "", nil
	}
	msg, err := messages.FetchMessage(ctx, client, t.Ref, false)
	if err != nil {
		return "", err
	}
	return cmp.Or(msg.ThreadTS, msg.TS), nil
}

// Located is a target resolved against Slack.
type Located struct {
	*clictx.Resolved
	// ChannelID is the conversation the target designates.
	ChannelID string
}

// WithTarget obtains a client for the target's workspace, resolves its
// conversation ID and runs work, once more with re-imported credentials
// if they turn out to be expired (clictx.WithAutoRefresh).
func WithTarget[T any](ctx context.Context, rt *clictx.Context, t Target, work func(loc Located) (T, error)) (T, error) {
	return clictx.WithAutoRefresh(ctx, rt, func() (T, error) {
		var zero T
		r, err := rt.Client(ctx, t.Workspace)
		if err != nil {
			return zero, err
		}
		channelID, err := t.ConversationID(ctx, r.Client)
		if err != nil {
			return zero, err
		}
		return work(Located{Resolved: r, ChannelID: channelID})
	})
}
