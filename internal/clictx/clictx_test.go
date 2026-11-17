package clictx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
)

// newContext returns a context with an empty credentials store and no
// credentials in the environment.
func newContext(t *testing.T) *Context {
	t.Helper()
	for _, name := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL", "CI", "SLACKCLI_SAFE_MODE", "AGENT_SLACK_SAFE_MODE"} {
		t.Setenv(name, "")
	}
	return &Context{
		Store:  &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
}

func failing(err error) Extractor {
	return func(context.Context) (*authextract.Extracted, error) { return nil, err }
}

func TestIsCI(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, " ": false, "0": false, "false": false, "FALSE": false, " False ": false,
		"1": true, "true": true, "yes": true, "github": true, "no": true,
	} {
		t.Setenv("CI", value)
		if got := IsCI(); got != want {
			t.Errorf("CI=%q: IsCI() = %v, want %v", value, got, want)
		}
	}
}

func TestSafeModeCoverage(t *testing.T) {
	c := newContext(t)
	commands := []string{"message edit", "message delete", "canvas create", "canvas edit", "workflow run"}

	summary := SafeModeSummary()
	for _, command := range append([]string{SafeModeReviewed}, commands...) {
		if !strings.Contains(summary, `"`+command+`"`) {
			t.Errorf("the summary %q does not mention %q", summary, command)
		}
	}

	for _, command := range commands {
		if err := c.CheckSafeMode(command); err != nil {
			t.Errorf("safe mode off: %q refused: %v", command, err)
		}
	}
	c.SafeModeFlag = true
	for _, command := range commands {
		err := c.CheckSafeMode(command)
		if err == nil {
			t.Errorf("safe mode on: %q allowed", command)
			continue
		}
		for _, want := range []string{"safe mode is active", "SLACKCLI_SAFE_MODE or --safe-mode", `"` + command + `" is blocked`, "without human review"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q does not contain %q", command, err, want)
			}
		}
	}
	// A command the list does not know is refused all the same.
	if err := c.CheckSafeMode("message purge"); err == nil || !strings.Contains(err.Error(), `"message purge" is blocked`) {
		t.Errorf("unknown command: err = %v", err)
	}
}

func TestClientWithoutCredentialsSaysWhyTheLookupFailed(t *testing.T) {
	notFound := fmt.Errorf("Slack Desktop: %w", authextract.ErrNotFound)

	t.Run("nothing installed is not noisy", func(t *testing.T) {
		c := newContext(t)
		c.Desktop, c.Chrome, c.Brave, c.Firefox = failing(notFound), failing(authextract.ErrNotFound), failing(authextract.ErrNotFound), failing(notFound)
		_, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "no Slack credentials available") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "lookup failed") || strings.Contains(err.Error(), "no Slack session found") {
			t.Errorf("the error mentions sources that simply have nothing: %v", err)
		}
	})

	t.Run("real failures are reported", func(t *testing.T) {
		c := newContext(t)
		c.Desktop = failing(errors.New("Slack Safe Storage: timed out (a keychain prompt may be waiting for you)"))
		c.Chrome = failing(authextract.ErrNotFound)
		c.Brave = failing(authextract.ErrBraveAppleScriptDisabled)
		c.Firefox = nil

		_, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "no Slack credentials available") {
			t.Fatalf("err = %v", err)
		}
		for _, want := range []string{
			"The automatic lookup failed for: Slack Desktop: Slack Safe Storage: timed out",
			"; Brave: Brave has JavaScript from Apple Events disabled",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "Chrome") {
			t.Errorf("the error mentions Chrome, which has nothing: %v", err)
		}

		// The same goes for a workspace that is asked for by name.
		_, err = c.Client(context.Background(), "acme")
		if err == nil || !strings.Contains(err.Error(), `no Slack credentials for workspace "acme"`) || !strings.Contains(err.Error(), "Slack Desktop: Slack Safe Storage: timed out") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a later source still succeeds", func(t *testing.T) {
		c := newContext(t)
		c.Desktop = failing(errors.New("keychain timed out"))
		c.Chrome = func(context.Context) (*authextract.Extracted, error) {
			return &authextract.Extracted{CookieD: "xoxd-1", Teams: []authextract.Team{{URL: "https://acme.slack.com/", Token: "xoxc-1"}}}, nil
		}
		r, err := c.Client(context.Background(), "")
		if err != nil || r.WorkspaceURL != "https://acme.slack.com" {
			t.Errorf("got %+v, %v", r, err)
		}
	})
}
