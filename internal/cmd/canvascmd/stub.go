// Package canvascmd implements the "canvas" command.
package canvascmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "canvas" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "canvas", Hidden: true}
}
