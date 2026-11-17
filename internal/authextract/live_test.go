package authextract

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// TestLiveExtract runs the file-based extractors against this machine's
// real Slack Desktop and Firefox data, read-only. It is skipped unless
// SLACKCLI_LIVE_EXTRACT=1, and never prints more of a secret than its
// length and a 4-character prefix. Chrome and Brave are left out: they
// drive the user's browser.
func TestLiveExtract(t *testing.T) {
	if os.Getenv(appinfo.EnvName("LIVE_EXTRACT")) != "1" {
		t.Skipf("set %s=1 to run against this machine's applications", appinfo.EnvName("LIVE_EXTRACT"))
	}
	prefix := func(s string) string { return s[:min(4, len(s))] }
	for name, extract := range map[string]func(context.Context) (*Extracted, error){
		"Desktop": Desktop,
		"Firefox": Firefox,
	} {
		t.Run(name, func(t *testing.T) {
			extracted, err := extract(t.Context())
			if errors.Is(err, ErrNotFound) {
				t.Skipf("no session: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("source %q, %d team(s), cookie %s… (%d chars)", extracted.Source, len(extracted.Teams), prefix(extracted.CookieD), len(extracted.CookieD))
			for _, team := range extracted.Teams {
				t.Logf("team %s %q, token %s… (%d chars)", team.URL, team.Name, prefix(team.Token), len(team.Token))
			}
			if len(extracted.Teams) == 0 || prefix(extracted.CookieD) != "xoxd" {
				t.Error("incomplete extraction")
			}
		})
	}
}
