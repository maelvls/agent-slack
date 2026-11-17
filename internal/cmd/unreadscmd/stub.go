// Package unreadscmd implements the "unreads" command.
package unreadscmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "unreads" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "unreads", Hidden: true}
}
