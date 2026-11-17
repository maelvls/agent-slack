// Package usercmd implements the "user" command.
package usercmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "user" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "user", Hidden: true}
}
