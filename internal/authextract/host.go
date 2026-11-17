package authextract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// host is what the extractors need from the machine they run on. Tests
// replace it to point the extractors at fixture directories and to fake
// the keychain and AppleScript.
type host struct {
	// goos is runtime.GOOS.
	goos string
	// home is the user's home directory.
	home   string
	getenv func(string) string
	// run executes a helper program (security, secret-tool, osascript) and
	// returns its trimmed standard output.
	run func(ctx context.Context, name string, args ...string) (string, error)
}

func localHost() (*host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locating the home directory: %w", err)
	}
	return &host{goos: runtime.GOOS, home: home, getenv: os.Getenv, run: runCommand}, nil
}

// commandError is a failed helper program.
type commandError struct {
	Name     string
	ExitCode int // -1 if the program did not run or was killed
	Stderr   string
	Err      error
}

func (e *commandError) Error() string {
	if msg := strings.TrimSpace(e.Stderr); msg != "" {
		return fmt.Sprintf("%s: %s", e.Name, msg)
	}
	return fmt.Sprintf("%s: %v", e.Name, e.Err)
}

func (e *commandError) Unwrap() error { return e.Err }

// runCommand never receives a secret in args: secrets only flow back
// through the program's standard output pipe.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		ce := &commandError{Name: name, ExitCode: -1, Stderr: stderr.String(), Err: err}
		if ctxErr := ctx.Err(); ctxErr != nil {
			ce.Err = ctxErr
			ce.Stderr = ""
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			ce.ExitCode = exit.ExitCode()
		}
		return "", ce
	}
	return strings.TrimSpace(stdout.String()), nil
}

// notFoundf builds an error that wraps ErrNotFound: the application is not
// there or holds no Slack session, as opposed to an extraction failure.
func notFoundf(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrNotFound)
}

// joinAttempts merges the errors of several candidate locations. The
// result wraps ErrNotFound only when every candidate was merely absent, so
// that a real failure (e.g. a cookie that cannot be decrypted) is never
// reported as "not found".
func joinAttempts(what string, errs []error) error {
	if len(errs) == 1 {
		return fmt.Errorf("%s: %w", what, errs[0])
	}
	allNotFound := true
	lines := make([]string, len(errs))
	for i, err := range errs {
		if !errors.Is(err, ErrNotFound) {
			allNotFound = false
		}
		lines[i] = strings.TrimSuffix(err.Error(), ": "+ErrNotFound.Error())
	}
	detail := strings.Join(lines, "; ")
	if allNotFound {
		return notFoundf("%s: %s", what, detail)
	}
	return fmt.Errorf("%s: %s", what, detail)
}
