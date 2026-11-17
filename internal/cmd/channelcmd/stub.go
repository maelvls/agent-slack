// Package channelcmd implements the "channel" command.
package channelcmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "channel" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "channel", Hidden: true}
}
