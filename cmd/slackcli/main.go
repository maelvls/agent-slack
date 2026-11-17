// Command slackcli is a Slack automation CLI for AI agents.
package main

import (
	"os"

	"github.com/maelvls/slackcli/internal/cmd/root"
)

func main() {
	os.Exit(root.Run(os.Args[1:], os.Stdout, os.Stderr))
}
