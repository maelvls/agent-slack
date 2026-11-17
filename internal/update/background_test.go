package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// runCheck performs one "invocation": start the check, pretend the command
// ran, and return what the notice printed.
func (f *fixture) runCheck(t *testing.T) string {
	t.Helper()
	var stderr bytes.Buffer
	f.u.StartBackgroundCheck(context.Background())(&stderr)
	return stderr.String()
}

// waitForStoredResult waits for a check that outlived its invocation to
// write its result to the state file.
func (f *fixture) waitForStoredResult(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.u.loadState().LatestVersion == "" {
		if time.Now().After(deadline) {
			t.Fatal("the result of the check was never stored")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantNotice(t *testing.T, got, current, latest string) {
	t.Helper()
	want := "\nUpdate available: " + current + " → " + latest + `. Run "` + appinfo.Name + ` update" to upgrade.` + "\n"
	if got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}
}

func TestBackgroundCheckNotifiesOncePerDay(t *testing.T) {
	f := newFixture(t)
	f.srv.Publish("v1.2.0", nil)

	wantNotice(t, f.runCheck(t), "1.0.0", "1.2.0")

	// Within 24 hours: neither a request nor a notice.
	f.clock = f.clock.Add(23 * time.Hour)
	if got := f.runCheck(t); got != "" {
		t.Errorf("second notice within 24h: %q", got)
	}
	if got := f.srv.APICalls(); got != 1 {
		t.Errorf("API calls = %d, want 1", got)
	}

	// The next day: checked again, and reminded with the newer release.
	f.srv.Publish("v1.3.0", nil)
	f.clock = f.clock.Add(2 * time.Hour)
	wantNotice(t, f.runCheck(t), "1.0.0", "1.3.0")
	if got := f.srv.APICalls(); got != 2 {
		t.Errorf("API calls = %d, want 2", got)
	}
}

func TestBackgroundCheckUpToDate(t *testing.T) {
	f := newFixture(t)
	f.srv.Publish("v1.0.0", nil)
	for range 3 {
		if got := f.runCheck(t); got != "" {
			t.Errorf("notice although up to date: %q", got)
		}
	}
	if got := f.srv.APICalls(); got != 1 {
		t.Errorf("API calls = %d, want 1 (throttled)", got)
	}
}

func TestBackgroundCheckNotifiesGoInstallUsers(t *testing.T) {
	f := newFixture(t)
	f.u.Version, f.u.ModuleVersion = "dev", "v1.0.0"
	f.srv.Publish("v1.2.0", nil)
	got := f.runCheck(t)
	if want := `Run "go install github.com/acme/tool/cmd/` + appinfo.Name + `@latest" to upgrade.`; !strings.Contains(got, want) {
		t.Errorf("notice = %q, want it to contain %q", got, want)
	}
}

func TestBackgroundCheckSkipped(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *fixture)
	}{
		{"opt-out variable", func(t *testing.T, f *fixture) { t.Setenv(appinfo.EnvName("NO_UPDATE_CHECK"), "1") }},
		{"opt-out variable spelled true", func(t *testing.T, f *fixture) { t.Setenv(appinfo.EnvName("NO_UPDATE_CHECK"), "true") }},
		{"legacy opt-out variable", func(t *testing.T, f *fixture) { t.Setenv(appinfo.LegacyEnvPrefix+"NO_UPDATE_CHECK", "1") }},
		{"CI", func(t *testing.T, f *fixture) { t.Setenv("CI", "true") }},
		{"dev build", func(t *testing.T, f *fixture) { f.u.Version, f.u.ModuleVersion = "dev", "(devel)" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.srv.Publish("v1.2.0", nil)
			tt.setup(t, f)
			if got := f.runCheck(t); got != "" {
				t.Errorf("notice = %q, want none", got)
			}
			if got := f.srv.APICalls(); got != 0 {
				t.Errorf("API calls = %d, want 0", got)
			}
			if _, err := os.Stat(f.u.StateDir); !os.IsNotExist(err) {
				t.Errorf("state directory was created (%v)", err)
			}
		})
	}
}

func TestBackgroundCheckSilentWhenStderrIsNotATerminal(t *testing.T) {
	f := newFixture(t)
	f.u.IsTerminal = isTerminal // the real one: a bytes.Buffer is not a terminal
	f.srv.Publish("v1.2.0", nil)
	if got := f.runCheck(t); got != "" {
		t.Errorf("notice written to a non-terminal: %q", got)
	}
	// The notice was not consumed: a later interactive run still shows it.
	f.waitForStoredResult(t)
	f.u.IsTerminal = func(io.Writer) bool { return true }
	wantNotice(t, f.runCheck(t), "1.0.0", "1.2.0")
}

func TestStartBackgroundCheckProductionEntryPoint(t *testing.T) {
	// Under "go test" the version is "dev" (and stderr is usually not a
	// terminal), so the production entry point must stay inert and must
	// not touch the network.
	if appinfo.Version != DevVersion {
		t.Skip("stamped build")
	}
	var stderr bytes.Buffer
	StartBackgroundCheck(context.Background())(&stderr)
	if stderr.Len() != 0 {
		t.Errorf("notice = %q, want none", stderr.String())
	}
}

func TestBackgroundCheckNeverBlocksTheCommand(t *testing.T) {
	f := newFixture(t)
	f.u.Grace = 20 * time.Millisecond
	f.srv.Publish("v1.2.0", nil)
	unblock := f.srv.BlockAPI()
	defer unblock()

	finish := f.u.StartBackgroundCheck(context.Background())
	var stderr bytes.Buffer
	start := time.Now()
	finish(&stderr)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("finish took %v with a hanging server", d)
	}
	if stderr.Len() != 0 {
		t.Errorf("notice = %q before the check completed", stderr.String())
	}

	// Once the slow check completes, its result is stored and the next
	// invocation reports it without asking GitHub again.
	unblock()
	f.waitForStoredResult(t)
	wantNotice(t, f.runCheck(t), "1.0.0", "1.2.0")
	if got := f.srv.APICalls(); got != 1 {
		t.Errorf("API calls = %d, want 1", got)
	}
}

func TestBackgroundCheckBacksOffAfterFailure(t *testing.T) {
	for name, fail := range map[string]func(f *fixture){
		"rate limit":   func(f *fixture) { f.srv.FailAPI(http.StatusForbidden, http.Header{"X-Ratelimit-Remaining": {"0"}}) },
		"server error": func(f *fixture) { f.srv.FailAPI(http.StatusInternalServerError, nil) },
		"no release":   func(f *fixture) {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			fail(f)
			// The predecessor retried on every invocation after a failure.
			for range 3 {
				if got := f.runCheck(t); got != "" {
					t.Errorf("notice after a failed check: %q", got)
				}
			}
			if got := f.srv.APICalls(); got != 1 {
				t.Errorf("API calls = %d, want 1", got)
			}

			// An hour later it tries again, and this time it works.
			f.srv.FailAPI(0, nil)
			f.srv.Publish("v1.2.0", nil)
			f.clock = f.clock.Add(61 * time.Minute)
			wantNotice(t, f.runCheck(t), "1.0.0", "1.2.0")
		})
	}
}

func TestBackgroundCheckSkippedWhenStateIsUnwritable(t *testing.T) {
	f := newFixture(t)
	f.srv.Publish("v1.2.0", nil)
	// A regular file where the state directory should be: nothing can be
	// stored, so the check could not be throttled.
	if err := os.WriteFile(f.u.StateDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := f.runCheck(t); got != "" {
			t.Errorf("notice = %q, want none", got)
		}
	}
	if got := f.srv.APICalls(); got != 0 {
		t.Errorf("API calls = %d, want 0: an unthrottled check would run on every invocation", got)
	}
}

func TestBackgroundCheckIgnoresCorruptState(t *testing.T) {
	for name, content := range map[string]string{
		"not json":            "{",
		"terminal escape":     `{"latest_version":"9.9.9\u001b[2J","checked_at":1767323045000,"attempted_at":1767323045000}`,
		"timestamp in future": `{"latest_version":"0.0.1","checked_at":9999999999999,"attempted_at":9999999999999,"notified_at":9999999999999}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.MkdirAll(f.u.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.u.StateDir, stateFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			f.srv.Publish("v1.2.0", nil)
			got := f.runCheck(t)
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("notice carries an escape sequence from the state file: %q", got)
			}
			if name != "terminal escape" {
				wantNotice(t, got, "1.0.0", "1.2.0")
			}
		})
	}
}

func TestStateFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permission bits")
	}
	f := newFixture(t)
	f.srv.Publish("v1.2.0", nil)
	f.runCheck(t)
	for path, want := range map[string]os.FileMode{
		f.u.StateDir:                           0o700,
		filepath.Join(f.u.StateDir, stateFile): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %v, want %v", path, got, want)
		}
	}
	entries, _ := os.ReadDir(f.u.StateDir)
	if len(entries) != 1 {
		t.Errorf("state directory holds %d entries, want only %s", len(entries), stateFile)
	}
}
