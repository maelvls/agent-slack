// Package updatecmd implements the "update" command.
package updatecmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/update"
)

const (
	// checkTimeout bounds the "what is the latest release" request.
	checkTimeout = 15 * time.Second
	// applyTimeout bounds the download; the command is exempt from the
	// global command timeout, so it needs a limit of its own.
	applyTimeout = 10 * time.Minute
)

// checkResult is printed when nothing was installed.
type checkResult struct {
	Current         string        `json:"current"`
	Latest          string        `json:"latest"`
	UpdateAvailable bool          `json:"update_available"`
	InstallMethod   update.Method `json:"install_method"`
	// UpdateCommand is the command that updates this installation. It is
	// absent when there is none to name (a binary put there by a package
	// manager): UpdateHint then says what to do.
	UpdateCommand string `json:"update_command"`
	UpdateHint    string `json:"update_hint"`
	Status        string `json:"status"`
}

// setAdvice fills in how to update an installation of the given method.
func (r *checkResult) setAdvice(u *update.Updater, method update.Method) {
	r.UpdateCommand = u.UpdateCommand(method)
	if r.UpdateCommand == "" {
		r.UpdateHint = u.UpdateAdvice(method)
	}
}

// updatedResult is printed after a successful self-update.
type updatedResult struct {
	Status          string        `json:"status"`
	InstallMethod   update.Method `json:"install_method"`
	PreviousVersion string        `json:"previous_version"`
	NewVersion      string        `json:"new_version"`
	Message         string        `json:"message"`
}

// New returns the "update" command.
func New(rt *clictx.Context) *cobra.Command { return newCommand(rt, update.New()) }

func newCommand(rt *clictx.Context, u *update.Updater) *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: fmt.Sprintf("Update %s to the latest version", appinfo.Name),
		Long: fmt.Sprintf(`Update %[1]s to the latest GitHub release.

A release binary (installed with install.sh or downloaded by hand) replaces
itself: the archive for this platform is downloaded, verified against the
release's SHA-256 checksums and swapped in atomically. Other installations
are not modified: the command fails and says what to do instead. For
"go install" and a build from source that is the "go install" command to run.
A binary found in a package manager's directory (a Homebrew Cellar, the Nix
store) must be reinstalled the way it was installed; %[1]s itself is only
distributed through install.sh, release archives and "go install".

Prints JSON with "status": "up_to_date", "update_available" (--check only),
"updated", or "development_build" (--check on a build without a version).
With --check, "update_command" is the command that updates this installation,
or "update_hint" says what to do when there is no such command.

%[1]s also checks for a new release in the background, at most once a
day and only when stderr is a terminal (never under CI). Disable that with
%[2]s=1.`,
			appinfo.Name, appinfo.EnvName("NO_UPDATE_CHECK")),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), rt, u, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates (don't install)")
	return clictx.NoTimeout(cmd)
}

func run(ctx context.Context, rt *clictx.Context, u *update.Updater, checkOnly bool) error {
	method := u.InstallMethod()

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	st, err := u.Check(checkCtx)
	cancel()
	if err != nil {
		return err
	}
	result := checkResult{
		Current:         st.Current,
		Latest:          st.Latest,
		UpdateAvailable: st.UpdateAvailable,
		InstallMethod:   method,
	}

	// A build without a version cannot be compared with a release; saying
	// "up to date" would be a guess.
	if st.Current == update.DevVersion {
		if !checkOnly {
			return fmt.Errorf("this %s is a development build without a version, so it cannot update itself; to install the latest release, %s",
				appinfo.Name, u.UpdateAdvice(method))
		}
		result.setAdvice(u, method)
		result.Status = "development_build"
		return rt.Print(result)
	}

	if !st.UpdateAvailable {
		result.Status = "up_to_date"
		return rt.Print(result)
	}
	if checkOnly {
		result.setAdvice(u, method)
		result.Status = "update_available"
		return rt.Print(result)
	}
	if method != update.MethodBinary {
		return fmt.Errorf("%s %s was installed with %s and cannot replace itself; to get %s, %s",
			appinfo.Name, st.Current, method, st.Latest, u.UpdateAdvice(method))
	}

	rt.Hintf("Updating %s %s → %s...", appinfo.Name, st.Current, st.Latest)
	applyCtx, cancel := context.WithTimeout(ctx, applyTimeout)
	defer cancel()
	path, err := u.Apply(applyCtx, st.Tag)
	if err != nil {
		return err
	}
	return rt.Print(updatedResult{
		Status:          "updated",
		InstallMethod:   method,
		PreviousVersion: st.Current,
		NewVersion:      st.Latest,
		Message:         fmt.Sprintf("Updated %s to %s (%s)", appinfo.Name, st.Latest, path),
	})
}
