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

// New builds the root command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:           appinfo.Name,
		Short:         appinfo.Description,
		Version:       appinfo.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.CompletionOptions.HiddenDefaultCmd = true
	cmd.PersistentFlags().BoolVar(&rt.SafeModeFlag, "safe-mode", false,
		fmt.Sprintf(`Keep a human in the loop: %s (also: %s=1)`, clictx.SafeModeSummary(), appinfo.EnvName("SAFE_MODE")))

	cmd.AddCommand(
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
	)
	return cmd
}

// Run executes the CLI with the given arguments (without the program name)
// and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	rt := clictx.New()
	rt.Stdout, rt.Stderr = stdout, stderr

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run the root's PersistentPreRunE even if a sub-command defines its own.
	cobra.EnableTraverseRunHooks = true

	cmd := New(rt)
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

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	var err error
	select {
	case err = <-done:
		stopWatchdog()
	case <-timedOut:
		fmt.Fprintf(stderr, "%s command timed out after %s. Set %s (milliseconds) to adjust.\n",
			appinfo.Name, timeout, appinfo.EnvName("COMMAND_TIMEOUT_MS"))
		return ExitTimeout
	}

	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
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
