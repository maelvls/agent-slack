package root

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
)

// result is what one run of the CLI produced.
type result struct {
	code           int
	stdout, stderr string
	rt             *clictx.Context
}

// runWith runs the real root (flags, watchdog, exit codes) over the given
// top-level commands, with an empty credentials store.
func runWith(t *testing.T, grace time.Duration, build func(rt *clictx.Context) []*cobra.Command, args ...string) result {
	t.Helper()
	for _, name := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_WORKSPACE_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv(appinfo.EnvName("NO_UPDATE_CHECK"), "1")
	var stdout, stderr bytes.Buffer
	var rt *clictx.Context
	r := runner{
		newContext: func() *clictx.Context {
			rt = &clictx.Context{Store: &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}}
			return rt
		},
		commands: build,
		grace:    grace,
	}
	code := r.run(args, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String(), rt: rt}
}

// one wraps a single test command.
func one(cmd *cobra.Command) func(*clictx.Context) []*cobra.Command {
	return func(*clictx.Context) []*cobra.Command { return []*cobra.Command{cmd} }
}

func TestRunSuccessAndErrors(t *testing.T) {
	build := func(rt *clictx.Context) []*cobra.Command {
		return []*cobra.Command{
			{Use: "ok", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
				return rt.Print(map[string]any{"ok": true})
			}},
			{Use: "fail", RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("resolving the channel: %w", errors.New("channel_not_found"))
			}},
		}
	}

	got := runWith(t, time.Second, build, "ok")
	if got.code != ExitOK || got.stdout != "{\n  \"ok\": true\n}\n" || got.stderr != "" {
		t.Errorf("ok: %+v", got)
	}

	// The error is printed once, on stderr, without usage.
	got = runWith(t, time.Second, build, "fail")
	if got.code != ExitError || got.stdout != "" || got.stderr != "resolving the channel: channel_not_found\n" {
		t.Errorf("fail: %+v", got)
	}

	// A command line cobra rejects points at --help.
	got = runWith(t, time.Second, build, "nope")
	if got.code != ExitError || !strings.Contains(got.stderr, `unknown command "nope"`) ||
		!strings.Contains(got.stderr, `Run "`+appinfo.Name+` --help" for usage.`) {
		t.Errorf("unknown command: %+v", got)
	}
	got = runWith(t, time.Second, build, "ok", "--nope")
	if got.code != ExitError || !strings.Contains(got.stderr, "unknown flag: --nope") || !strings.Contains(got.stderr, "--help") {
		t.Errorf("unknown flag: %+v", got)
	}
	// A command that ran and failed on its arguments does not.
	got = runWith(t, time.Second, build, "ok", "extra")
	if got.code != ExitError || !strings.Contains(got.stderr, "unknown command") {
		t.Errorf("extra argument: %+v", got)
	}

	// No arguments: help, successfully.
	got = runWith(t, time.Second, build)
	if got.code != ExitOK || !strings.Contains(got.stdout, "Available Commands") || !strings.Contains(got.stdout, "--safe-mode") {
		t.Errorf("no arguments: %+v", got)
	}
}

func TestRunTimeoutCancelsTheCommandAndLetsItCleanUp(t *testing.T) {
	t.Setenv(appinfo.EnvName("COMMAND_TIMEOUT_MS"), "40")
	var cancelled, cleaned atomic.Bool
	slow := &cobra.Command{Use: "slow", RunE: func(cmd *cobra.Command, _ []string) error {
		defer cleaned.Store(true) // a deferred cleanup, e.g. removing a temp file
		select {
		case <-cmd.Context().Done():
			cancelled.Store(true)
			return cmd.Context().Err()
		case <-time.After(10 * time.Second):
			return nil
		}
	}}
	start := time.Now()
	got := runWith(t, 5*time.Second, one(slow), "slow")
	if got.code != ExitTimeout {
		t.Fatalf("exit code = %d, want %d (%+v)", got.code, ExitTimeout, got)
	}
	if !cancelled.Load() || !cleaned.Load() {
		t.Errorf("context cancelled = %v, cleanup ran = %v", cancelled.Load(), cleaned.Load())
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("returned after %s: it should not wait for the whole grace period", elapsed)
	}
	for _, want := range []string{
		appinfo.Name + " command timed out after 40ms",
		"may still have gone through: check before retrying",
		"SLACKCLI_COMMAND_TIMEOUT_MS",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr %q does not contain %q", got.stderr, want)
		}
	}
	// The cancellation is reported as a timeout, not as an interruption.
	if strings.Contains(got.stderr, "interrupted") || strings.Count(got.stderr, "\n") != 1 {
		t.Errorf("stderr = %q", got.stderr)
	}
}

func TestRunTimeoutDoesNotWaitForACommandThatIgnoresCancellation(t *testing.T) {
	t.Setenv(appinfo.EnvName("COMMAND_TIMEOUT_MS"), "30")
	release := make(chan struct{})
	finished := make(chan struct{})
	stuck := &cobra.Command{Use: "stuck", RunE: func(*cobra.Command, []string) error {
		defer close(finished)
		<-release
		return nil
	}}
	start := time.Now()
	got := runWith(t, 50*time.Millisecond, one(stuck), "stuck")
	if got.code != ExitTimeout || time.Since(start) > 2*time.Second {
		t.Errorf("code = %d after %s", got.code, time.Since(start))
	}
	close(release)
	<-finished
}

func TestRunNoTimeout(t *testing.T) {
	t.Setenv(appinfo.EnvName("COMMAND_TIMEOUT_MS"), "30")
	wait := func(*cobra.Command, []string) error {
		time.Sleep(150 * time.Millisecond)
		return nil
	}

	// A command marked NoTimeout waits on a human: no watchdog.
	got := runWith(t, time.Second, one(clictx.NoTimeout(&cobra.Command{Use: "edit", RunE: wait})), "edit")
	if got.code != ExitOK || got.stderr != "" {
		t.Errorf("NoTimeout: %+v", got)
	}

	// A command can also decide so at run time.
	got = runWith(t, time.Second, func(rt *clictx.Context) []*cobra.Command {
		return []*cobra.Command{{Use: "send", RunE: func(cmd *cobra.Command, args []string) error {
			rt.DisableTimeout()
			return wait(cmd, args)
		}}}
	}, "send")
	if got.code != ExitOK || got.stderr != "" {
		t.Errorf("DisableTimeout: %+v", got)
	}

	// Without either, the same command times out.
	got = runWith(t, time.Second, one(&cobra.Command{Use: "send", RunE: wait}), "send")
	if got.code != ExitTimeout {
		t.Errorf("plain command: %+v", got)
	}
}

// --safe-mode turns safe mode on, and --safe-mode=false cannot turn off a
// safe mode that the environment turned on.
func TestRunSafeModeFlagCannotDisableTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env, legacy string
		args        []string
		want        bool
	}{
		{"", "", []string{"probe"}, false},
		{"", "", []string{"probe", "--safe-mode"}, true},
		{"", "", []string{"--safe-mode", "probe"}, true},
		{"", "", []string{"probe", "--safe-mode=false"}, false},
		{"1", "", []string{"probe"}, true},
		{"1", "", []string{"probe", "--safe-mode=false"}, true},
		{"1", "", []string{"--safe-mode=false", "probe"}, true},
		{"enabled", "", []string{"probe", "--safe-mode=false"}, true},
		{"false", "1", []string{"probe", "--safe-mode=false"}, true},
		{"0", "", []string{"probe", "--safe-mode"}, true},
	} {
		t.Setenv("SLACKCLI_SAFE_MODE", tc.env)
		t.Setenv("AGENT_SLACK_SAFE_MODE", tc.legacy)
		got := runWith(t, time.Second, func(rt *clictx.Context) []*cobra.Command {
			return []*cobra.Command{{Use: "probe", RunE: func(*cobra.Command, []string) error {
				return rt.Print(map[string]any{"safe_mode": rt.SafeMode(), "blocked": rt.CheckSafeMode("message delete") != nil})
			}}}
		}, tc.args...)
		want := fmt.Sprintf("{\n  \"blocked\": %[1]v,\n  \"safe_mode\": %[1]v\n}\n", tc.want)
		if got.code != ExitOK || got.stdout != want {
			t.Errorf("SLACKCLI_SAFE_MODE=%q AGENT_SLACK_SAFE_MODE=%q %v: %+v, want safe mode %v", tc.env, tc.legacy, tc.args, got, tc.want)
		}
	}
}

// -V and --version print the bare version, as the predecessor did, even
// though release builds are stamped with the "v" of the git tag.
func TestVersionFlags(t *testing.T) {
	old := appinfo.Version
	t.Cleanup(func() { appinfo.Version = old })
	appinfo.Version = "v1.2.3"

	for _, flag := range []string{"-V", "--version", "-v"} {
		got := runWith(t, time.Second, commands, flag)
		if got.code != ExitOK || got.stdout != "1.2.3\n" || got.stderr != "" {
			t.Errorf("%s: %+v", flag, got)
		}
	}
	help := runWith(t, time.Second, commands, "--help").stdout
	if !strings.Contains(help, "-V, --version") || strings.Contains(help, "version-short") {
		t.Errorf("help does not advertise -V, or shows the hidden alias:\n%s", help)
	}
}

// The real command tree builds, and every top-level command is there.
func TestCommandTree(t *testing.T) {
	cmd := New(&clictx.Context{Store: &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}})
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	want := "auth canvas channel later message search unreads update user workflow"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("commands = %s, want %s", got, want)
	}
	if !isUpdateCommand(mustFind(t, cmd, "update")) || isUpdateCommand(mustFind(t, cmd, "auth", "test")) {
		t.Error("isUpdateCommand is wrong")
	}
}

func mustFind(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestErrorMessage(t *testing.T) {
	if ErrorMessage(nil) != "" || ErrorMessage(context.DeadlineExceeded) != "context deadline exceeded" {
		t.Error("ErrorMessage")
	}
}
