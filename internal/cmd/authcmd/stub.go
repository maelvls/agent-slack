// Package authcmd implements the "auth" command.
package authcmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "auth" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "auth", Hidden: true}
}
