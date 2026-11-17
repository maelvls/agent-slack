// Package updatecmd implements the "update" command.
package updatecmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "update" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "update", Hidden: true}
}
