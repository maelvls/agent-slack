package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/update/updatetest"
)

// fakeWget stands in for GNU wget where there is no curl. Like GitHub, it
// answers a release URL with a redirect to $FAKE_REDIRECT/<file>, which it
// only reports (headers on stderr, exit status 8) because install.sh runs
// it with --max-redirect=0; any other URL serves $FAKE_RELEASE/<file>.
// With FAKE_BUSYBOX set, its help does not know --max-redirect.
const fakeWget = `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--help" ]; then
    [ -n "${FAKE_BUSYBOX:-}" ] || echo "       --max-redirect              maximum redirections allowed per page"
    exit 0
  fi
done
url=""; dest=""; limited=""
while [ $# -gt 0 ]; do
  case "$1" in
    -O) dest=$2; shift ;;
    --max-redirect=0) limited=1 ;;
    http*) url=$1 ;;
  esac
  shift
done
printf '%s\n' "$url" >> "$FAKE_LOG"
case "$url" in
  https://github.com/*)
    if [ -z "$limited" ]; then
      # A wget left to follow redirects by itself would fetch this.
      printf '%s\n' "$FAKE_REDIRECT/${url##*/}" >> "$FAKE_LOG"
      exit 0
    fi
    : > "$dest"
    printf '  HTTP/1.1 302 Found\r\n  Location: %s\r\n  Content-Length: 0\r\n' "$FAKE_REDIRECT/${url##*/}" >&2
    exit 8
    ;;
esac
[ -f "$FAKE_RELEASE/${url##*/}" ] || { echo "  HTTP/1.1 404 Not Found" >&2; exit 8; }
cp "$FAKE_RELEASE/${url##*/}" "$dest"
`

// TestInstallScriptWithWget covers the wget fallback of install.sh: like
// the curl path, it must never follow a redirect to plain http.
func TestInstallScriptWithWget(t *testing.T) {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("install.sh is only exercised where it can install: Linux and macOS on amd64 and arm64")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}

	// A PATH with the tools install.sh needs, but no curl.
	bin := t.TempDir()
	for _, tool := range []string{"sh", "uname", "mktemp", "tar", "awk", "tr", "grep", "rm", "mkdir", "cp", "chmod", "mv"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for _, optional := range []string{"gzip", "sha256sum", "shasum", "openssl", "perl", "env"} {
		if path, err := exec.LookPath(optional); err == nil {
			_ = os.Symlink(path, filepath.Join(bin, optional))
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "wget"), []byte(fakeWget), 0o755); err != nil {
		t.Fatal(err)
	}

	releaseDir := t.TempDir()
	assets := map[string][]byte{}
	for _, goos := range []string{"darwin", "linux"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			assets[AssetName(goos, goarch)] = updatetest.TarGz(t, map[string][]byte{
				BinaryName(goos): []byte("#!/bin/sh\necho " + goos + "/" + goarch + "\n"),
			})
		}
	}
	assets[ChecksumsName] = updatetest.Checksums(assets)
	for name, content := range assets {
		if err := os.WriteFile(filepath.Join(releaseDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(t *testing.T, env ...string) (installDir, log, output string, err error) {
		home := t.TempDir()
		installDir = filepath.Join(home, "bin")
		logFile := filepath.Join(t.TempDir(), "log")
		cmd := exec.Command(filepath.Join(bin, "sh"), script)
		cmd.Env = append([]string{
			"PATH=" + bin,
			"HOME=" + home,
			"TMPDIR=" + t.TempDir(),
			"FAKE_RELEASE=" + releaseDir,
			"FAKE_LOG=" + logFile,
			appinfo.EnvName("INSTALL_DIR") + "=" + installDir,
		}, env...)
		out, err := cmd.CombinedOutput()
		logged, _ := os.ReadFile(logFile)
		return installDir, string(logged), string(out), err
	}
	const github = "https://github.com/" + appinfo.Repo + "/releases/latest/download/"

	t.Run("follows redirects to https", func(t *testing.T) {
		installDir, log, out, err := run(t, "FAKE_REDIRECT=https://objects.example/release")
		if err != nil {
			t.Fatalf("install.sh failed: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(installDir, appinfo.Name)); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{github + ChecksumsName + "\n", "https://objects.example/release/" + ChecksumsName + "\n"} {
			if !strings.Contains(log, want) {
				t.Errorf("downloads = %q, want %q among them", log, want)
			}
		}
	})

	t.Run("refuses a redirect to plain http", func(t *testing.T) {
		installDir, log, out, err := run(t, "FAKE_REDIRECT=http://objects.example/release")
		if err == nil || !strings.Contains(out, "only https is allowed") {
			t.Fatalf("install.sh: err = %v, output:\n%s", err, out)
		}
		if strings.Contains(log, "http://") {
			t.Errorf("a plain http URL was requested: %q", log)
		}
		if entries, _ := os.ReadDir(installDir); len(entries) != 0 {
			t.Errorf("installed %d file(s) from a download that left https", len(entries))
		}
	})

	t.Run("refuses a wget that cannot limit redirects", func(t *testing.T) {
		installDir, log, out, err := run(t, "FAKE_REDIRECT=http://objects.example/release", "FAKE_BUSYBOX=1")
		if err == nil || !strings.Contains(out, "install curl") {
			t.Fatalf("install.sh: err = %v, output:\n%s", err, out)
		}
		if log != "" {
			t.Errorf("downloads were attempted: %q", log)
		}
		if entries, _ := os.ReadDir(installDir); len(entries) != 0 {
			t.Errorf("installed %d file(s)", len(entries))
		}
	})
}
