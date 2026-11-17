// Package workflowcmd implements the "workflow" command.
package workflowcmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "workflow" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "workflow", Hidden: true}
}
