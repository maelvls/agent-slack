// Package update implements self-update from GitHub releases and the
// throttled background "new version available" notice.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// DevVersion is the value of appinfo.Version in a build that was not
// stamped by the release tooling.
const DevVersion = "dev"

// Method says how the running binary was installed, which decides how it
// can be updated.
type Method string

// Install methods.
const (
	// MethodBinary is a release binary (install.sh or a manual download):
	// the only method that self-updates.
	MethodBinary Method = "binary"
	// MethodGoInstall is a tagged version built by "go install …@version".
	MethodGoInstall Method = "go-install"
	// MethodHomebrew is a binary living in a Homebrew Cellar.
	MethodHomebrew Method = "homebrew"
	// MethodNix is a binary living in the Nix store.
	MethodNix Method = "nix"
	// MethodSource is an unversioned build from a checkout.
	MethodSource Method = "source"
)

// Updater checks for and installs new releases. Every external dependency
// is a field so that tests can replace it; New returns the production
// configuration.
type Updater struct {
	// HTTPClient performs every request. Deadlines come from the context.
	HTTPClient *http.Client
	// APIBaseURL is the GitHub REST API root (https://api.github.com).
	APIBaseURL string
	// DownloadBaseURL is the root under which release assets live
	// (https://github.com).
	DownloadBaseURL string
	// Repo is the GitHub "owner/name" publishing the releases.
	Repo string
	// Version is the version stamped at build time ("dev" when unstamped).
	Version string
	// ModuleVersion is the main module version recorded by the Go
	// toolchain; it identifies "go install …@v1.2.3" builds, which are
	// not stamped.
	ModuleVersion string
	// GOOS and GOARCH select the release asset.
	GOOS, GOARCH string
	// Now is the clock.
	Now func() time.Time
	// StateDir holds the update-check state file.
	StateDir string
	// Executable returns the path of the running binary.
	Executable func() (string, error)
	// IsTerminal reports whether notices written to w reach a human.
	IsTerminal func(w io.Writer) bool
	// Grace is how long the background notice waits, after the command
	// has finished, for a check that is still in flight.
	Grace time.Duration
}

// New returns the Updater for the running program.
func New() *Updater {
	u := &Updater{
		HTTPClient:      &http.Client{CheckRedirect: refuseDowngrade},
		APIBaseURL:      "https://api.github.com",
		DownloadBaseURL: "https://github.com",
		Repo:            appinfo.Repo,
		Version:         appinfo.Version,
		GOOS:            runtime.GOOS,
		GOARCH:          runtime.GOARCH,
		Now:             time.Now,
		StateDir:        appinfo.CacheDir(),
		Executable:      os.Executable,
		IsTerminal:      isTerminal,
		Grace:           300 * time.Millisecond,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		u.ModuleVersion = bi.Main.Version
	}
	return u
}

// refuseDowngrade stops a release download from being redirected from
// HTTPS to plain HTTP.
func refuseDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// isTerminal reports whether w is a character device such as a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// CurrentVersion returns the running version without "v" prefix, or "dev"
// when the build carries no release version.
func (u *Updater) CurrentVersion() string {
	for _, s := range []string{u.Version, u.ModuleVersion} {
		if s == DevVersion || !isReleaseVersion(s) {
			continue
		}
		if v, err := parseSemver(s); err == nil {
			return v.String()
		}
	}
	return DevVersion
}

// executablePath returns the real location of the running binary,
// following symlinks such as /opt/homebrew/bin/x → ../Cellar/….
func (u *Updater) executablePath() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// InstallMethod detects how the running binary was installed.
func (u *Updater) InstallMethod() Method {
	if exe, err := u.executablePath(); err == nil {
		p := filepath.ToSlash(exe)
		switch {
		case strings.HasPrefix(p, "/nix/store/"):
			return MethodNix
		case strings.Contains(p, "/Cellar/"):
			return MethodHomebrew
		}
	}
	switch {
	case u.Version != DevVersion && isReleaseVersion(u.Version):
		return MethodBinary
	case isReleaseVersion(u.ModuleVersion):
		return MethodGoInstall
	}
	return MethodSource
}

// goInstallCommand is how to (re)install from source with the Go toolchain.
func (u *Updater) goInstallCommand() string {
	return fmt.Sprintf("go install github.com/%s/cmd/%s@latest", u.Repo, appinfo.Name)
}

// installScriptCommand is the documented one-line installer.
func (u *Updater) installScriptCommand() string {
	return fmt.Sprintf("curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh", u.Repo)
}

// UpdateCommand returns the shell command that updates an installation of
// the given method.
func (u *Updater) UpdateCommand(m Method) string {
	switch m {
	case MethodGoInstall, MethodSource:
		return u.goInstallCommand()
	case MethodHomebrew:
		return "brew upgrade " + appinfo.Name
	case MethodNix:
		return "nix profile upgrade " + appinfo.Name
	}
	return appinfo.Name + " update"
}

// Status is the outcome of a version check. Versions carry no "v" prefix.
type Status struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	// Tag is the git tag of the latest release, to pass to Apply.
	Tag string `json:"-"`
}

// Check asks GitHub for the latest release, bypassing the 24-hour
// throttle of the background check. A "dev" build is never outdated.
func (u *Updater) Check(ctx context.Context) (Status, error) {
	rel, err := u.latestRelease(ctx)
	if err != nil {
		return Status{}, err
	}
	// Best effort: let the background notice benefit from this check.
	now := u.Now().UnixMilli()
	st := u.loadState()
	st.LatestVersion, st.CheckedAt, st.AttemptedAt = rel.version, now, now
	_ = u.saveState(st)

	current := u.CurrentVersion()
	return Status{
		Current:         current,
		Latest:          rel.version,
		UpdateAvailable: isNewer(rel.version, current),
		Tag:             rel.tag,
	}, nil
}
