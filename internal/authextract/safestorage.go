package authextract

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// keychainItem identifies a macOS generic password. An empty account
// matches any account of the service.
type keychainItem struct{ service, account string }

func (k keychainItem) String() string {
	if k.account == "" {
		return k.service
	}
	return k.service + "/" + k.account
}

// safeStorage finds the "Safe Storage" password a Chromium-based
// application encrypts its cookies with.
//
// Candidates are looked up lazily, one at a time, and the caller stops at
// the first that decrypts: every macOS keychain read may show the user a
// permission prompt, so no more items are read than necessary.
type safeStorage struct {
	h *host
	// macOS are the keychain items to try, in order.
	macOS []keychainItem
	// linux are secret-tool attribute pairs to try, in order.
	linux [][2]string

	results  map[string]string // lookup → password, "" if unavailable
	failures []string
}

// slackSafeStorage is where Slack Desktop keeps its password. The Electron
// and Mac App Store builds use different accounts of the same service.
func (h *host) slackSafeStorage() *safeStorage {
	return &safeStorage{
		h: h,
		macOS: []keychainItem{
			{"Slack Safe Storage", "Slack Key"},
			{"Slack Safe Storage", "Slack App Store Key"},
			{"Slack Safe Storage", ""},
		},
		linux: [][2]string{
			{"application", "com.slack.Slack"},
			{"application", "Slack"},
			{"application", "slack"},
			{"service", "Slack Safe Storage"},
		},
	}
}

func keychainTimeout() time.Duration {
	return appinfo.EnvDurationMS("KEYCHAIN_TIMEOUT_MS", 3*time.Second, false)
}

// securityItemNotFound is security(1)'s exit status for a missing item.
const securityItemNotFound = 44

func (s *safeStorage) lookup(ctx context.Context, label, program string, args ...string) string {
	if pw, done := s.results[label]; done {
		return pw
	}
	ctx, cancel := context.WithTimeout(ctx, keychainTimeout())
	defer cancel()
	pw, err := s.h.run(ctx, program, args...)
	if err != nil {
		var ce *commandError
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			s.failures = append(s.failures, fmt.Sprintf(
				"%s: timed out (a keychain prompt may be waiting for you: answer it, or raise %s, then retry)",
				label, appinfo.EnvName("KEYCHAIN_TIMEOUT_MS")))
		case errors.As(err, &ce) && program == "security" && ce.ExitCode == securityItemNotFound:
			s.failures = append(s.failures, label+": not in the keychain")
		default:
			s.failures = append(s.failures, fmt.Sprintf("%s: %v", label, err))
		}
		pw = ""
	}
	if s.results == nil {
		s.results = map[string]string{}
	}
	s.results[label] = pw
	return pw
}

// passwords yields candidate passwords for a cookie whose encrypted value
// starts with prefix ("v10", "v11" or anything else for legacy values).
func (s *safeStorage) passwords(ctx context.Context, prefix string) iter.Seq[string] {
	return func(yield func(string) bool) {
		seen := map[string]bool{}
		emit := func(pw string) bool {
			if seen[pw] {
				return true
			}
			seen[pw] = true
			return yield(pw)
		}
		switch s.h.goos {
		case "darwin":
			for _, item := range s.macOS {
				args := []string{"find-generic-password", "-w", "-s", item.service}
				if item.account != "" {
					args = append(args, "-a", item.account)
				}
				if pw := s.lookup(ctx, "keychain item "+item.String(), "security", args...); pw != "" && !emit(pw) {
					return
				}
			}
		case "linux":
			// Chromium's os_crypt on Linux: "v10" values use the fixed
			// password "peanuts", "v11" values a password from the
			// desktop keyring (or an empty one when the keyring was
			// unavailable at encryption time).
			if prefix != "v11" && !emit("peanuts") {
				return
			}
			for _, attr := range s.linux {
				label := "secret-tool " + attr[0] + "=" + attr[1]
				if pw := s.lookup(ctx, label, "secret-tool", "lookup", attr[0], attr[1]); pw != "" && !emit(pw) {
					return
				}
			}
			if !emit("") || !emit("peanuts") {
				return
			}
		}
	}
}

// failureSummary explains why passwords could not be obtained.
func (s *safeStorage) failureSummary() string {
	if len(s.failures) == 0 {
		return "none of the Safe Storage passwords found decrypts it"
	}
	return strings.Join(s.failures, "; ")
}
