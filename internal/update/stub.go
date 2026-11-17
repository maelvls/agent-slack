// Package update implements self-update and the throttled background
// "new version available" notice.
package update

// PLACEHOLDER: delete this file when the package is ported. The signature
// below is the contract used by the root command.

import (
	"context"
	"io"
)

// StartBackgroundCheck starts (at most once per 24h, never blocking the
// command) a check for a newer release. The returned function is called by
// the root command after the command has run; it prints a one-line notice
// to stderr if a newer version is known, and must return promptly.
func StartBackgroundCheck(ctx context.Context) (finish func(stderr io.Writer)) {
	return func(io.Writer) {}
}
