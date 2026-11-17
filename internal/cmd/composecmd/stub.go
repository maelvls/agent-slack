// Package composecmd implements the "compose" command.
package composecmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "compose" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "compose", Hidden: true}
}
