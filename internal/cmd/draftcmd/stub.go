// Package draftcmd implements the "draft" command.
package draftcmd

// PLACEHOLDER: delete this file when the command is ported.

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "draft" command.
func New(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{Use: "draft", Hidden: true}
}
