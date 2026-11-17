// Package appinfo holds the program's identity (name, version, env-var
// prefix, on-disk locations). Every other package derives user-visible names
// from here so that the tool can be renamed in one place.
package appinfo

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const (
	// Name is the binary name, config directory name and keychain service.
	Name = "slackcli"
	// EnvPrefix prefixes every tool-specific environment variable.
	EnvPrefix = "SLACKCLI_"
	// Repo is the GitHub "owner/name" used for releases and self-update.
	Repo = "maelvls/slackcli"
	// Description is the one-line summary shown in help output.
	Description = "Slack automation CLI for AI agents"

	// LegacyName and LegacyEnvPrefix identify the TypeScript predecessor
	// (agent-slack). They are only used to migrate credentials and to keep
	// honouring old environment variables.
	LegacyName      = "agent-slack"
	LegacyEnvPrefix = "AGENT_SLACK_"
)

// Version is set at build time with
// -ldflags "-X github.com/maelvls/slackcli/internal/appinfo.Version=v1.2.3".
var Version = "dev"

func init() {
	// `go install module/cmd/name@v1.2.3` builds carry no ldflags; take the
	// version from the module metadata instead.
	if Version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			Version = v
		}
	}
}

// UserAgent is sent on every HTTP request.
func UserAgent() string { return Name + "/" + strings.TrimPrefix(Version, "v") }

// EnvName returns the full name of a tool-specific environment variable,
// e.g. EnvName("SAFE_MODE") == "SLACKCLI_SAFE_MODE".
func EnvName(suffix string) string { return EnvPrefix + suffix }

// Env reads a tool-specific environment variable (trimmed), falling back to
// the legacy AGENT_SLACK_ prefix. It returns "" when unset or blank.
func Env(suffix string) string {
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + suffix)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(LegacyEnvPrefix + suffix))
}

// EnvDurationMS reads a millisecond duration from a tool-specific variable.
// It returns def when the variable is unset or invalid. Zero is only accepted
// when allowZero is true.
func EnvDurationMS(suffix string, def time.Duration, allowZero bool) time.Duration {
	raw := Env(suffix)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n < 0 || (n == 0 && !allowZero) {
		return def
	}
	return time.Duration(n) * time.Millisecond
}

// EnvBool reports whether a tool-specific variable is set to a truthy value
// ("1", "true", "yes", "on"; case-insensitive).
func EnvBool(suffix string) bool {
	switch strings.ToLower(Env(suffix)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return os.TempDir()
	}
	return h
}

// ConfigDir is where credentials.json lives: $XDG_CONFIG_HOME/slackcli or
// ~/.config/slackcli (on every OS, like the predecessor).
func ConfigDir() string {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, Name)
	}
	return filepath.Join(home(), ".config", Name)
}

// LegacyConfigDir is the predecessor's config directory.
func LegacyConfigDir() string { return filepath.Join(home(), ".config", LegacyName) }

// CacheDir holds regenerable state (user cache, update-check stamp,
// downloads): the OS user cache directory + "/slackcli".
func CacheDir() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, Name)
	}
	return filepath.Join(home(), "."+Name)
}

// DownloadsDir is where message attachments and canvases are downloaded.
// Paths printed to the user are absolute.
func DownloadsDir() string { return filepath.Join(CacheDir(), "downloads") }

// EnsureDownloadsDir creates DownloadsDir (0700) and returns it.
func EnsureDownloadsDir() (string, error) {
	d := DownloadsDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}
