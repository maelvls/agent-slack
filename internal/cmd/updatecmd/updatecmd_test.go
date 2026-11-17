package updatecmd

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
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/update"
	"github.com/maelvls/slackcli/internal/update/updatetest"
)

const testRepo = "acme/tool"

type fixture struct {
	u   *update.Updater
	srv *updatetest.Server
	exe string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	binDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		srv: updatetest.NewServer(t, testRepo),
		exe: filepath.Join(binDir, update.BinaryName(runtime.GOOS)),
	}
	if err := os.WriteFile(f.exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.u = &update.Updater{
		HTTPClient:      f.srv.Client(),
		APIBaseURL:      f.srv.URL,
		DownloadBaseURL: f.srv.URL,
		Repo:            testRepo,
		Version:         "v1.0.0",
		GOOS:            "linux",
		GOARCH:          "amd64",
		Now:             time.Now,
		StateDir:        filepath.Join(t.TempDir(), "state"),
		Executable:      func() (string, error) { return f.exe, nil },
		IsTerminal:      func(io.Writer) bool { return false },
	}
	return f
}

// publish releases tag with a real archive and checksums file.
func (f *fixture) publish(t *testing.T, tag string) {
	t.Helper()
	asset := update.AssetName(f.u.GOOS, f.u.GOARCH)
	assets := map[string][]byte{
		asset: updatetest.TarGz(t, map[string][]byte{update.BinaryName(f.u.GOOS): []byte("new binary")}),
	}
	assets[update.ChecksumsName] = updatetest.Checksums(assets)
	f.srv.Publish(tag, assets)
}

// run executes "update <args>" and returns stdout, stderr and the error.
func (f *fixture) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := newCommand(&clictx.Context{Stdout: &stdout, Stderr: &stderr}, f.u)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true // as the root does
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func (f *fixture) exeContent(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantJSON(t *testing.T, got string, lines ...string) {
	t.Helper()
	want := "{\n  " + strings.Join(lines, ",\n  ") + "\n}\n"
	if got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommandShape(t *testing.T) {
	cmd := New(&clictx.Context{})
	if cmd.Name() != "update" {
		t.Errorf("name = %q", cmd.Name())
	}
	if !clictx.HasNoTimeout(cmd) {
		t.Error("update must be exempt from the global command timeout: downloads can be slow")
	}
	flag := cmd.Flags().Lookup("check")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--check flag = %+v", flag)
	}
	if !strings.Contains(cmd.Long, appinfo.EnvName("NO_UPDATE_CHECK")) {
		t.Error("help does not document the opt-out variable")
	}
}

func TestRejectsArguments(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.run(t, "v1.2.3"); err == nil {
		t.Fatal("update accepted a positional argument")
	}
	if got := f.srv.APICalls(); got != 0 {
		t.Errorf("API calls = %d, want 0", got)
	}
}

func TestCheckUpdateAvailable(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "v1.2.0")
	stdout, stderr, err := f.run(t, "--check")
	if err != nil {
		t.Fatal(err)
	}
	wantJSON(t, stdout,
		`"current": "1.0.0"`,
		`"latest": "1.2.0"`,
		`"update_available": true`,
		`"install_method": "binary"`,
		`"update_command": "`+appinfo.Name+` update"`,
		`"status": "update_available"`,
	)
	if stderr != "" {
		t.Errorf("stderr = %q", stderr)
	}
	if got := f.exeContent(t); got != "old binary" {
		t.Errorf("--check modified the binary: %q", got)
	}
	if got := f.srv.Downloads(); len(got) != 0 {
		t.Errorf("--check downloaded %v", got)
	}
}

func TestUpToDate(t *testing.T) {
	for _, args := range [][]string{nil, {"--check"}} {
		f := newFixture(t)
		f.publish(t, "v1.0.0")
		stdout, _, err := f.run(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, stdout,
			`"current": "1.0.0"`,
			`"latest": "1.0.0"`,
			`"update_available": false`,
			`"install_method": "binary"`,
			`"status": "up_to_date"`,
		)
		if got := f.srv.Downloads(); len(got) != 0 {
			t.Errorf("downloaded %v although up to date", got)
		}
	}
}

func TestUpdateReplacesBinary(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "v1.2.0")
	stdout, stderr, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.exeContent(t); got != "new binary" {
		t.Errorf("binary content = %q, want the new binary", got)
	}
	wantJSON(t, stdout,
		`"status": "updated"`,
		`"install_method": "binary"`,
		`"previous_version": "1.0.0"`,
		`"new_version": "1.2.0"`,
		`"message": "Updated `+appinfo.Name+` to 1.2.0 (`+strings.ReplaceAll(f.exe, `\`, `\\`)+`)"`,
	)
	if want := "Updating " + appinfo.Name + " 1.0.0 → 1.2.0...\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestUpdateFailsOnChecksumMismatch(t *testing.T) {
	f := newFixture(t)
	asset := update.AssetName("linux", "amd64")
	f.srv.Publish("v1.2.0", map[string][]byte{
		asset:                []byte("tampered"),
		update.ChecksumsName: updatetest.Checksums(map[string][]byte{asset: []byte("original")}),
	})
	stdout, _, err := f.run(t)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing on failure", stdout)
	}
	if got := f.exeContent(t); got != "old binary" {
		t.Errorf("binary content = %q, want it untouched", got)
	}
}

func TestOtherInstallMethodsAreNotModified(t *testing.T) {
	goInstall := "go install github.com/acme/tool/cmd/" + appinfo.Name + "@latest"
	tests := []struct {
		name        string
		setup       func(f *fixture)
		method      string
		wantCommand string
	}{
		{"go install", func(f *fixture) { f.u.Version, f.u.ModuleVersion = "dev", "v1.0.0" }, "go-install", goInstall},
		{"homebrew", func(f *fixture) {
			f.u.Executable = func() (string, error) { return "/opt/homebrew/Cellar/tool/1.0.0/bin/tool", nil }
		}, "homebrew", "brew upgrade " + appinfo.Name},
		{"nix", func(f *fixture) {
			f.u.Executable = func() (string, error) { return "/nix/store/abc-tool-1.0.0/bin/tool", nil }
		}, "nix", "nix profile upgrade " + appinfo.Name},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.publish(t, "v1.2.0")
			tt.setup(f)

			stdout, _, err := f.run(t)
			if err == nil || !strings.Contains(err.Error(), "run: "+tt.wantCommand) || !strings.Contains(err.Error(), "installed with "+tt.method) {
				t.Fatalf("error = %v, want it to give %q", err, tt.wantCommand)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing on failure", stdout)
			}
			if got := f.srv.Downloads(); len(got) != 0 {
				t.Errorf("downloaded %v", got)
			}
			if got := f.exeContent(t); got != "old binary" {
				t.Errorf("binary content = %q, want it untouched", got)
			}

			stdout, _, err = f.run(t, "--check")
			if err != nil {
				t.Fatal(err)
			}
			wantJSON(t, stdout,
				`"current": "1.0.0"`,
				`"latest": "1.2.0"`,
				`"update_available": true`,
				`"install_method": "`+tt.method+`"`,
				`"update_command": "`+tt.wantCommand+`"`,
				`"status": "update_available"`,
			)
		})
	}
}

func TestDevelopmentBuild(t *testing.T) {
	f := newFixture(t)
	f.u.Version, f.u.ModuleVersion = "dev", "(devel)"
	f.publish(t, "v1.2.0")
	goInstall := "go install github.com/acme/tool/cmd/" + appinfo.Name + "@latest"

	stdout, _, err := f.run(t, "--check")
	if err != nil {
		t.Fatal(err)
	}
	wantJSON(t, stdout,
		`"current": "dev"`,
		`"latest": "1.2.0"`,
		`"update_available": false`,
		`"install_method": "source"`,
		`"update_command": "`+goInstall+`"`,
		`"status": "development_build"`,
	)

	_, _, err = f.run(t)
	if err == nil || !strings.Contains(err.Error(), "development build") || !strings.Contains(err.Error(), goInstall) {
		t.Fatalf("error = %v, want an explanation with %q", err, goInstall)
	}
	if got := f.exeContent(t); got != "old binary" {
		t.Errorf("binary content = %q, want it untouched", got)
	}
}

func TestCheckFailureIsAnError(t *testing.T) {
	f := newFixture(t)
	f.srv.FailAPI(http.StatusForbidden, http.Header{"X-Ratelimit-Remaining": {"0"}})
	stdout, _, err := f.run(t, "--check")
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error = %v, want the rate limit to be reported", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q: a failed check must not look like a result", stdout)
	}
}
