// Package composecmd implements "message compose": a local, browser-based
// editor in which a human reviews, edits and sends a message.
package composecmd

// PLACEHOLDER: delete this file when the command is ported. The signatures
// below are the contract messagecmd already relies on.

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// Options are the settings of a compose session.
type Options struct {
	// Workspace is the effective --workspace selector ("" for default).
	Workspace string
	// ThreadTS, when set, makes the message a reply in that thread.
	ThreadTS string
	// NoUnfurl suppresses link and media previews.
	NoUnfurl bool
}

// Compose opens the editor for target (message URL, #channel, channel ID)
// pre-filled with initialText and blocks until the human sends or cancels.
// The returned value is the JSON payload to print (e.g. {ok, sent} or
// {ok, cancelled}). It is also what safe mode redirects "message send" to;
// callers must have disabled the command timeout (rt.DisableTimeout).
func Compose(ctx context.Context, rt *clictx.Context, target, initialText string, opts Options) (map[string]any, error) {
	return nil, errors.New("compose: not implemented")
}

// New returns the "compose" command, to be added under "message".
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "compose", Hidden: true}
}
