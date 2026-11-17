package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/update/updatetest"
)

const testRepo = "acme/tool"

type fixture struct {
	u     *Updater
	srv   *updatetest.Server
	exe   string
	clock time.Time
}

// newFixture returns an Updater at version 1.0.0 wired to a fake GitHub,
// a temporary state directory and a fake "running binary".
func newFixture(t *testing.T) *fixture {
	t.Helper()
	// The environment of the developer or of CI must not decide the tests.
	t.Setenv("CI", "")
	t.Setenv(appinfo.EnvName("NO_UPDATE_CHECK"), "")
	t.Setenv(appinfo.LegacyEnvPrefix+"NO_UPDATE_CHECK", "")

	// Resolved, because the updater reports the real path of the binary
	// and macOS temporary directories live behind a symlink.
	binDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		srv:   updatetest.NewServer(t, testRepo),
		exe:   filepath.Join(binDir, BinaryName(runtime.GOOS)),
		clock: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := os.WriteFile(f.exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.u = &Updater{
		HTTPClient:      f.srv.Client(),
		APIBaseURL:      f.srv.URL,
		DownloadBaseURL: f.srv.URL,
		Repo:            testRepo,
		Version:         "v1.0.0",
		GOOS:            "linux",
		GOARCH:          "amd64",
		Now:             func() time.Time { return f.clock },
		StateDir:        filepath.Join(t.TempDir(), "state"),
		Executable:      func() (string, error) { return f.exe, nil },
		IsTerminal:      func(io.Writer) bool { return true },
		Grace:           5 * time.Second,
	}
	return f
}

// publish releases tag with an archive of binary for the fixture's
// platform, plus the checksums file.
func (f *fixture) publish(t *testing.T, tag, binary string) {
	t.Helper()
	asset := AssetName(f.u.GOOS, f.u.GOARCH)
	files := map[string][]byte{"LICENSE": []byte("MIT"), BinaryName(f.u.GOOS): []byte(binary)}
	assets := map[string][]byte{}
	if strings.HasSuffix(asset, ".zip") {
		assets[asset] = updatetest.Zip(t, files)
	} else {
		assets[asset] = updatetest.TarGz(t, files)
	}
	assets[ChecksumsName] = updatetest.Checksums(assets)
	f.srv.Publish(tag, assets)
}

func (f *fixture) exeContent(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNoLeftovers checks that only the binary remains in its directory.
func (f *fixture) assertNoLeftovers(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(f.exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(f.exe) {
			t.Errorf("leftover file next to the binary: %s", e.Name())
		}
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		moduleVersion string
		tag           string
		want          Status
	}{
		{"newer release", "v1.0.0", "", "v1.2.0", Status{Current: "1.0.0", Latest: "1.2.0", UpdateAvailable: true, Tag: "v1.2.0"}},
		{"same release", "v1.2.0", "", "v1.2.0", Status{Current: "1.2.0", Latest: "1.2.0", Tag: "v1.2.0"}},
		{"version stamped without v", "1.0.0", "", "v1.2.0", Status{Current: "1.0.0", Latest: "1.2.0", UpdateAvailable: true, Tag: "v1.2.0"}},
		{"running a pre-release of the latest", "v1.2.0-rc.1", "", "v1.2.0", Status{Current: "1.2.0-rc.1", Latest: "1.2.0", UpdateAvailable: true, Tag: "v1.2.0"}},
		{"running something newer than the latest", "v1.3.0-rc.1", "", "v1.2.0", Status{Current: "1.3.0-rc.1", Latest: "1.2.0", Tag: "v1.2.0"}},
		{"dev build is never outdated", "dev", "(devel)", "v1.2.0", Status{Current: "dev", Latest: "1.2.0", Tag: "v1.2.0"}},
		{"dev build from a dirty checkout", "dev", "v1.0.1-0.20260102150405-0123456789ab+dirty", "v1.2.0", Status{Current: "dev", Latest: "1.2.0", Tag: "v1.2.0"}},
		{"go install of a tag", "dev", "v1.0.0", "v1.2.0", Status{Current: "1.0.0", Latest: "1.2.0", UpdateAvailable: true, Tag: "v1.2.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.u.Version, f.u.ModuleVersion = tt.version, tt.moduleVersion
			f.srv.Publish(tt.tag, nil)
			got, err := f.u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Check() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Publish("v9.9.9", nil)
		reset := strconv.FormatInt(f.clock.Add(10*time.Minute).Unix(), 10)
		f.srv.FailAPI(http.StatusForbidden, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}})
		_, err := f.u.Check(context.Background())
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("Check() error = %v, want a *RateLimitError", err)
		}
		if rl.RetryAfter != 10*time.Minute || !strings.Contains(err.Error(), "try again in 10m0s") {
			t.Errorf("RetryAfter = %v, message %q", rl.RetryAfter, err)
		}
	})
	t.Run("secondary rate limit", func(t *testing.T) {
		f := newFixture(t)
		f.srv.FailAPI(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}})
		_, err := f.u.Check(context.Background())
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
			t.Fatalf("Check() error = %v, want a *RateLimitError of 30s", err)
		}
	})
	t.Run("plain 403 is not a rate limit", func(t *testing.T) {
		f := newFixture(t)
		f.srv.FailAPI(http.StatusForbidden, nil)
		_, err := f.u.Check(context.Background())
		var rl *RateLimitError
		if err == nil || errors.As(err, &rl) || !strings.Contains(err.Error(), "HTTP 403") {
			t.Fatalf("Check() error = %v, want a plain HTTP 403 error", err)
		}
	})
	t.Run("no release", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.u.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no published release") {
			t.Fatalf("Check() error = %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		f := newFixture(t)
		f.srv.FailAPI(http.StatusBadGateway, nil)
		_, err := f.u.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
			t.Fatalf("Check() error = %v", err)
		}
	})
	t.Run("tag that is not a version", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Publish("../../evil", nil)
		_, err := f.u.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not a version") {
			t.Fatalf("Check() error = %v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Close()
		if _, err := f.u.Check(context.Background()); err == nil {
			t.Fatal("Check() succeeded against a closed server")
		}
	})
}

func TestInstallMethod(t *testing.T) {
	tests := []struct {
		name          string
		exe           string
		version       string
		moduleVersion string
		want          Method
		wantCommand   string
	}{
		{"release binary", "/home/me/.local/bin/" + appinfo.Name, "v1.0.0", "(devel)", MethodBinary, appinfo.Name + " update"},
		{"go install of a tag", "/home/me/go/bin/" + appinfo.Name, "dev", "v1.0.0", MethodGoInstall, "go install github.com/acme/tool/cmd/" + appinfo.Name + "@latest"},
		{"go build in a checkout", "/src/" + appinfo.Name, "dev", "(devel)", MethodSource, "go install github.com/acme/tool/cmd/" + appinfo.Name + "@latest"},
		{"go install of a commit", "/home/me/go/bin/" + appinfo.Name, "dev", "v1.0.1-0.20260102150405-0123456789ab", MethodSource, "go install github.com/acme/tool/cmd/" + appinfo.Name + "@latest"},
		{"homebrew", "/opt/homebrew/Cellar/" + appinfo.Name + "/1.0.0/bin/" + appinfo.Name, "v1.0.0", "", MethodHomebrew, "brew upgrade " + appinfo.Name},
		{"nix", "/nix/store/abc-" + appinfo.Name + "-1.0.0/bin/" + appinfo.Name, "v1.0.0", "", MethodNix, "nix profile upgrade " + appinfo.Name},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &Updater{
				Repo:          testRepo,
				Version:       tt.version,
				ModuleVersion: tt.moduleVersion,
				Executable:    func() (string, error) { return tt.exe, nil },
			}
			got := u.InstallMethod()
			if got != tt.want {
				t.Errorf("InstallMethod() = %q, want %q", got, tt.want)
			}
			if cmd := u.UpdateCommand(got); cmd != tt.wantCommand {
				t.Errorf("UpdateCommand(%q) = %q, want %q", got, cmd, tt.wantCommand)
			}
		})
	}
}

func TestInstallMethodFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "Cellar", "tool", "1.0.0", "bin", "tool")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tool")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	u := &Updater{Version: "v1.0.0", Executable: func() (string, error) { return link, nil }}
	if got := u.InstallMethod(); got != MethodHomebrew {
		t.Errorf("InstallMethod() = %q, want %q", got, MethodHomebrew)
	}
}

func TestFindChecksum(t *testing.T) {
	const sum = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	sums := "0000  short\n" +
		sum + "  tool_linux_amd64.tar.gz\n" +
		strings.ToUpper(sum) + " *tool_darwin_arm64.tar.gz\n" +
		strings.Repeat("z", 64) + "  bad\n"
	for name, wantErr := range map[string]string{
		"tool_linux_amd64.tar.gz":  "",
		"tool_darwin_arm64.tar.gz": "",
		"tool_linux_arm64.tar.gz":  "no entry",
		"amd64.tar.gz":             "no entry",
		"short":                    "malformed",
		"bad":                      "malformed",
	} {
		got, err := findChecksum([]byte(sums), name)
		switch {
		case wantErr == "" && (err != nil || got != sum):
			t.Errorf("findChecksum(%q) = %q, %v; want %q", name, got, err, sum)
		case wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)):
			t.Errorf("findChecksum(%q) error = %v, want %q", name, err, wantErr)
		}
	}
}
