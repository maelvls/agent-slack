package cmdutil

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

const (
	// TargetWorkspaceUsage is the help of --workspace on commands that
	// take a <target> (see Workspace).
	TargetWorkspaceUsage = "Workspace selector (full URL or unique substring). Needed for #channel/channel-ID/user-ID targets when several workspaces are configured; a Slack URL target names its own workspace, which --workspace may repeat but not contradict"
	// MessageTSUsage is the help of --ts on commands that act on one
	// message (see Target.MessageTS).
	MessageTSUsage = "Message ts, e.g. 1770165109.628379 (required when <target> is a channel; redundant with a message URL, which it must not contradict)"
)

// AddTargetWorkspaceFlag registers --workspace on a command that takes a
// <target>.
func AddTargetWorkspaceFlag(cmd *cobra.Command, workspace *string) {
	cmd.Flags().StringVar(workspace, "workspace", "", TargetWorkspaceUsage)
}

// ParseTS validates the value of a flag holding a Slack message timestamp
// and returns it trimmed; "" stays "". flag is the flag's name, dashes
// included.
func ParseTS(flag, raw string) (string, error) {
	ts := strings.TrimSpace(raw)
	if ts != "" && !slackurl.IsTS(ts) {
		return "", fmt.Errorf(`invalid %s value %q: expected a Slack message timestamp such as 1770165109.628379 (the "ts" of a message)`, flag, raw)
	}
	return ts, nil
}

// ParseLimit parses the value of an optional --limit flag declared as a
// string, so that "not given" can mean "whatever Slack defaults to": ""
// returns 0, anything else must be a positive integer.
func ParseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid --limit value %q: must be a positive integer", raw)
	}
	return n, nil
}

// CheckLimit validates a --limit flag that must be between 1 and max.
// Values above max are refused rather than clamped, so that the caller
// knows the result is not all it asked for; more says how to get the rest.
func CheckLimit(n, max int, more string) error {
	if n < 1 || n > max {
		return fmt.Errorf("invalid --limit value %d: must be between 1 and %d (%s)", n, max, more)
	}
	return nil
}

// CheckMaxChars validates a flag that caps how many characters of a text
// are printed: a positive integer, or -1 for no cap. 0 is refused rather
// than taken for "the default" or "nothing". flag is the flag's name,
// dashes included.
func CheckMaxChars(flag string, n int) error {
	if n == 0 || n < -1 {
		return fmt.Errorf("invalid %s value %d: must be a positive integer, or -1 for unlimited", flag, n)
	}
	return nil
}

// AttachPaths trims and de-duplicates the values of a repeatable --attach
// flag, keeping their order.
func AttachPaths(raw []string) []string {
	var out []string
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}
