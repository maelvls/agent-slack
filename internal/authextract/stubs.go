package authextract

// PLACEHOLDER: these stubs only exist so that the rest of the program
// compiles while the extractors are being ported. Delete this file once the
// real implementations exist. The signatures are the contract.

import (
	"context"
	"fmt"
)

// Desktop extracts credentials from the Slack desktop app's local data
// (macOS, Linux, Windows). Slack does not need to be quit.
func Desktop(ctx context.Context) (*Extracted, error) {
	return nil, fmt.Errorf("Slack Desktop: %w", ErrNotFound)
}

// Chrome extracts credentials from a logged-in Slack tab in Google Chrome
// (macOS, via AppleScript).
func Chrome(ctx context.Context) (*Extracted, error) {
	return nil, fmt.Errorf("Chrome: %w", ErrNotFound)
}

// Brave extracts credentials from a logged-in Slack tab in Brave (macOS).
// It returns ErrBraveAppleScriptDisabled when Brave refuses AppleScript.
func Brave(ctx context.Context) (*Extracted, error) {
	return nil, fmt.Errorf("Brave: %w", ErrNotFound)
}

// Firefox extracts credentials from Firefox profile storage.
func Firefox(ctx context.Context) (*Extracted, error) {
	return nil, fmt.Errorf("Firefox: %w", ErrNotFound)
}
