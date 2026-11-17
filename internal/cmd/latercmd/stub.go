// Package latercmd implements the "later" command.
package latercmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "later" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "later", Hidden: true}
}
