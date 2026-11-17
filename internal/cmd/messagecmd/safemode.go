package messagecmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/cmd/composecmd"
)

// Safe mode keeps a human in the loop for what an agent writes to Slack:
// "message send" is redirected to the compose editor, "message edit" and
// "message delete" are refused. See clictx.Context.SafeMode.

// safeModeBlockedError is returned by "edit" and "delete" in safe mode.
func safeModeBlockedError(action string) error {
	return fmt.Errorf(`safe mode is active (%[1]s or --safe-mode): "message %[2]s" is blocked so an agent cannot %[2]s messages without human review. Disable safe mode to %[2]s messages`,
		appinfo.EnvName("SAFE_MODE"), action)
}

// redirectFlags says which of the send flags that carry a value were
// given (an explicitly empty value counts).
type redirectFlags struct {
	attach, blocks, schedule, scheduleIn bool
}

// redirectSendToCompose hands a "message send" to the compose editor, so
// that a human reviews and sends it. Flags the editor cannot represent are
// refused rather than silently dropped; so is CI, where the editor cannot
// open and compose would otherwise send straight away.
func (c *command) redirectSendToCompose(ctx context.Context, targetInput, text string, opts sendOptions, set redirectFlags) (map[string]any, error) {
	var unsupported []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"--attach", set.attach},
		{"--blocks", set.blocks},
		{"--schedule", set.schedule},
		{"--schedule-in", set.scheduleIn},
		{"--reply-broadcast", opts.replyBroadcast},
	} {
		if f.set {
			unsupported = append(unsupported, f.name)
		}
	}
	if len(unsupported) > 0 {
		return nil, fmt.Errorf(`safe mode is active: "message send" is redirected to the compose editor, which does not support %s. Drop those flags or disable safe mode`, strings.Join(unsupported, ", "))
	}
	if os.Getenv("CI") != "" {
		return nil, errors.New("safe mode is active but the interactive compose editor is unavailable in CI (the CI environment variable is set). Disable safe mode, or unset CI, to send messages")
	}

	c.rt.Hintf(`Safe mode active: redirecting "message send" to the compose editor. Nothing posts until a human sends it from the editor.`)
	// The editor waits on a human for as long as it takes.
	c.rt.DisableTimeout()
	payload, err := c.compose(ctx, c.rt, targetInput, text, composecmd.Options{
		Workspace: c.rt.EffectiveWorkspace(opts.workspace),
		ThreadTS:  strings.TrimSpace(opts.threadTS),
		NoUnfurl:  opts.noUnfurl,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"safe_mode": true, "redirected_from": "send"}
	maps.Copy(out, payload)
	return out, nil
}
