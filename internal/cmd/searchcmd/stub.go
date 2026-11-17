// Package searchcmd implements the "search" command.
package searchcmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "search" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "search", Hidden: true}
}
