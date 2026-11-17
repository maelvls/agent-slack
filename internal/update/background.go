package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

const (
	// checkInterval is how often GitHub is asked for the latest release,
	// and how often the notice is repeated.
	checkInterval = 24 * time.Hour
	// retryInterval spaces out attempts after a check that failed or did
	// not finish before the process exited.
	retryInterval = time.Hour
	// backgroundTimeout bounds the background request.
	backgroundTimeout = 5 * time.Second

	stateFile = "update-check.json"
)

// state is the content of the update-check state file. Times are Unix
// milliseconds.
type state struct {
	LatestVersion string `json:"latest_version,omitempty"`
	// CheckedAt is the last successful check, AttemptedAt the last attempt
	// (successful or not), NotifiedAt the last time the notice was shown.
	CheckedAt   int64 `json:"checked_at,omitempty"`
	AttemptedAt int64 `json:"attempted_at,omitempty"`
	NotifiedAt  int64 `json:"notified_at,omitempty"`
}

// loadState returns the zero state when the file is missing or corrupt.
func (u *Updater) loadState() state {
	var st state
	b, err := os.ReadFile(filepath.Join(u.StateDir, stateFile))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return state{}
	}
	return st
}

// saveState writes the state file atomically, so that concurrent
// invocations never read a half-written file.
func (u *Updater) saveState(st state) error {
	if err := os.MkdirAll(u.StateDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(u.StateDir, stateFile+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(u.StateDir, stateFile))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// elapsed reports whether at least d has passed since the Unix-millisecond
// timestamp since. A timestamp in the future (clock change) counts as
// elapsed so that a bad value cannot silence checks forever.
func elapsed(now time.Time, since int64, d time.Duration) bool {
	delta := now.UnixMilli() - since
	return delta < 0 || delta >= d.Milliseconds()
}

// backgroundDisabled reports whether the user or the environment opted out
// of the background check.
func backgroundDisabled() bool {
	if appinfo.EnvBool("NO_UPDATE_CHECK") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CI"))) {
	case "", "0", "false":
		return false
	}
	return true
}

func noNotice(io.Writer) {}

// StartBackgroundCheck is the entry point used by the root command: it
// runs the background check of the production Updater.
//
// The check is skipped entirely (no request, no notice) when
// SLACKCLI_NO_UPDATE_CHECK is truthy, when CI is set, when stderr is not a
// terminal — an agent or a script gains nothing from the notice — and for
// "dev" builds.
func StartBackgroundCheck(ctx context.Context) (finish func(stderr io.Writer)) {
	// Checked before New so that the common non-interactive invocation
	// costs one fstat.
	if !isTerminal(os.Stderr) {
		return noNotice
	}
	return New().StartBackgroundCheck(ctx)
}

// StartBackgroundCheck starts, at most once per 24 hours and without ever
// blocking the command, a check for a newer release. The returned function
// is to be called once the command has run: it prints a one-line notice to
// stderr (at most once per 24 hours) if a newer version is known, waiting
// no longer than u.Grace for a check that is still in flight; a result
// that arrives later is reported by a later invocation. Errors are
// swallowed: the check never interferes with the command.
func (u *Updater) StartBackgroundCheck(ctx context.Context) (finish func(stderr io.Writer)) {
	current := u.CurrentVersion()
	if current == DevVersion || backgroundDisabled() {
		return noNotice
	}

	var (
		mu   sync.Mutex // guards st and the state file
		st   = u.loadState()
		now  = u.Now()
		done chan struct{}
	)
	if elapsed(now, st.CheckedAt, checkInterval) && elapsed(now, st.AttemptedAt, retryInterval) {
		// Record the attempt before making it. If the state cannot be
		// written the check is skipped: it could not be throttled, and
		// would hit GitHub on every single invocation.
		st.AttemptedAt = now.UnixMilli()
		if u.saveState(st) == nil {
			done = make(chan struct{})
			go func() {
				defer close(done)
				ctx, cancel := context.WithTimeout(ctx, backgroundTimeout)
				defer cancel()
				rel, err := u.latestRelease(ctx)
				if err != nil {
					return // retried after retryInterval
				}
				mu.Lock()
				defer mu.Unlock()
				st.LatestVersion, st.CheckedAt = rel.version, now.UnixMilli()
				_ = u.saveState(st)
			}()
		}
	}

	return func(stderr io.Writer) {
		if !u.IsTerminal(stderr) {
			return
		}
		if done != nil {
			grace := time.NewTimer(u.Grace)
			defer grace.Stop()
			select {
			case <-done:
			case <-grace.C:
			}
		}
		mu.Lock()
		defer mu.Unlock()
		// Parsing also guarantees that what comes out of the state file
		// is safe to print on a terminal.
		latest, err := parseSemver(st.LatestVersion)
		if err != nil || !isNewer(st.LatestVersion, current) || !elapsed(now, st.NotifiedAt, checkInterval) {
			return
		}
		fmt.Fprintf(stderr, "\nUpdate available: %s → %s. Run \"%s\" to upgrade.\n",
			current, latest, u.UpdateCommand(u.InstallMethod()))
		st.NotifiedAt = now.UnixMilli()
		_ = u.saveState(st)
	}
}
