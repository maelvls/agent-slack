// Package root assembles the command tree and runs it.
package root

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/authcmd"
	"github.com/maelvls/slackcli/internal/cmd/canvascmd"
	"github.com/maelvls/slackcli/internal/cmd/channelcmd"
	"github.com/maelvls/slackcli/internal/cmd/latercmd"
	"github.com/maelvls/slackcli/internal/cmd/messagecmd"
	"github.com/maelvls/slackcli/internal/cmd/searchcmd"
	"github.com/maelvls/slackcli/internal/cmd/unreadscmd"
	"github.com/maelvls/slackcli/internal/cmd/updatecmd"
	"github.com/maelvls/slackcli/internal/cmd/usercmd"
	"github.com/maelvls/slackcli/internal/cmd/workflowcmd"
	"github.com/maelvls/slackcli/internal/slack/enrich"
	"github.com/maelvls/slackcli/internal/update"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitTimeout = 124
)

const defaultCommandTimeout = 30 * time.Second

// CommandTimeout is the global per-command time limit
// (SLACKCLI_COMMAND_TIMEOUT_MS; default 30s).
func CommandTimeout() time.Duration {
	return appinfo.EnvDurationMS("COMMAND_TIMEOUT_MS", defaultCommandTimeout, false)
}

// timeoutGrace is how long a command that timed out is given to notice
// that its context was cancelled and return, so that its deferred cleanups
// run (temporary copies of cookie databases, partial downloads…).
const timeoutGrace = 2 * time.Second

// New builds the root command.
func New(rt *clictx.Context) *cobra.Command {
	return newRoot(rt, commands(rt))
}

// commands returns the top-level commands.
func commands(rt *clictx.Context) []*cobra.Command {
	return []*cobra.Command{
		authcmd.New(rt),
		messagecmd.New(rt),
		canvascmd.New(rt),
		searchcmd.New(rt),
		latercmd.New(rt),
		unreadscmd.New(rt),
		usercmd.New(rt),
		channelcmd.New(rt),
		workflowcmd.New(rt),
		updatecmd.New(rt),
	}
}

// newRoot builds the root command (global flags, version) over the given
// top-level commands.
func newRoot(rt *clictx.Context, subcommands []*cobra.Command) *cobra.Command {
	var shortVersion bool
	cmd := &cobra.Command{
		Use:   appinfo.Name,
		Short: appinfo.Description,
		// Without the "v" of the git tag, like "update --check" and the
		// User-Agent.
		Version:       appinfo.BareVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if shortVersion {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), cmd.Version)
				return err
			}
			return cmd.Help()
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.CompletionOptions.HiddenDefaultCmd = true
	// -V is the predecessor's shorthand. Defining the flag here keeps
	// cobra from adding its own with -v, which stays as a hidden alias.
	cmd.Flags().BoolP("version", "V", false, "Print the version of "+appinfo.Name)
	cmd.Flags().BoolVarP(&shortVersion, "version-short", "v", false, "Print the version (alias of -V)")
	_ = cmd.Flags().MarkHidden("version-short")
	cmd.PersistentFlags().BoolVar(&rt.SafeModeFlag, "safe-mode", false,
		fmt.Sprintf(`Keep a human in the loop: %s (also: %s=1; --safe-mode=false does not turn it off when the environment turns it on)`, clictx.SafeModeSummary(), appinfo.EnvName("SAFE_MODE")))

	cmd.AddCommand(subcommands...)
	return cmd
}

// Run executes the CLI with the given arguments (without the program name)
// and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return runner{newContext: clictx.New, commands: commands, grace: timeoutGrace}.run(args, stdout, stderr)
}

// runner is Run with its dependencies spelled out, so that tests can run
// the real root (flags, watchdog, exit codes) over commands of their own.
type runner struct {
	newContext func() *clictx.Context
	commands   func(rt *clictx.Context) []*cobra.Command
	// grace is how long a command gets to return after a timeout.
	grace time.Duration
}

func (r runner) run(args []string, stdout, stderr io.Writer) int {
	rt := r.newContext()
	rt.Stdout, rt.Stderr = stdout, stderr

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// cancel is how a timeout tells the command to stop.
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	// Run the root's PersistentPreRunE even if a sub-command defines its own.
	cobra.EnableTraverseRunHooks = true

	cmd := newRoot(rt, r.commands(rt))
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	// The watchdog bounds every command that does not wait on a human, so
	// that an agent is never left hanging on a stuck network call.
	timeout := CommandTimeout()
	var (
		once     sync.Once
		timedOut = make(chan struct{})
		watchdog *time.Timer
	)
	stopWatchdog := func() {
		once.Do(func() {
			if watchdog != nil {
				watchdog.Stop()
			}
		})
	}
	rt.SetWatchdogCanceller(stopWatchdog)

	var ran *cobra.Command
	finishUpdateCheck := func(io.Writer) {}
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		ran = c
		if clictx.HasNoTimeout(c) {
			return nil
		}
		watchdog = time.AfterFunc(timeout, func() { close(timedOut) })
		if !isUpdateCommand(c) && c.HasParent() {
			finishUpdateCheck = update.StartBackgroundCheck(c.Context())
		}
		return nil
	}

	// Optional lookups that Slack rate limited do not fail a command: they
	// report here, and the user is told the result is incomplete.
	ctx, incomplete := enrich.Collect(ctx)

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	var err error
	select {
	case err = <-done:
		stopWatchdog()
	case <-timedOut:
		// The command is still running: what it was doing may or may not
		// have reached Slack, and saying "timed out" alone invites a
		// retry that posts twice.
		fmt.Fprintf(stderr, "%s command timed out after %s. If it was sending or changing something, that may still have gone through: check before retrying. Set %s (milliseconds) to adjust.\n",
			appinfo.Name, timeout, appinfo.EnvName("COMMAND_TIMEOUT_MS"))
		// Let the command see the cancellation and return, so that its
		// deferred cleanups run; do not wait for one that ignores it.
		cancel()
		grace := time.NewTimer(r.grace)
		defer grace.Stop()
		select {
		case <-done:
		case <-grace.C:
		}
		return ExitTimeout
	}

	if err != nil {
		if errors.Is(err, context.Canceled) && signalCtx.Err() != nil {
			fmt.Fprintln(stderr, "interrupted")
			return 130
		}
		fmt.Fprintln(stderr, ErrorMessage(err))
		if ran == nil {
			// No command ran: cobra rejected the command line itself.
			fmt.Fprintf(stderr, "Run \"%s --help\" for usage.\n", appinfo.Name)
		}
		return ExitError
	}
	for _, w := range incomplete.List() {
		fmt.Fprintln(stderr, "Warning: "+w)
	}
	finishUpdateCheck(stderr)
	return ExitOK
}

func isUpdateCommand(c *cobra.Command) bool {
	for ; c != nil; c = c.Parent() {
		if c.Name() == "update" && c.Parent() != nil && !c.Parent().HasParent() {
			return true
		}
	}
	return false
}

// ErrorMessage renders an error for the user on a single line.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
