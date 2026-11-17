package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/update/updatetest"
)

// repoFile reads a file at the root of the repository.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("%v (this test ties internal/update to the release tooling and needs the repository root)", err)
	}
	return string(b)
}

func mustContain(t *testing.T, file, content, want string) {
	t.Helper()
	if !strings.Contains(content, want) {
		t.Errorf("%s does not contain %q", file, want)
	}
}

// TestReleaseNamingMatchesTooling fails when the asset names self-update
// downloads (AssetName, BinaryName, ChecksumsName) and the names the
// release tooling produces (.goreleaser.yaml) or the installer expects
// (install.sh) drift apart, and when a rename of the tool was not carried
// over to the non-Go files.
func TestReleaseNamingMatchesTooling(t *testing.T) {
	const file = ".goreleaser.yaml"
	cfg := repoFile(t, file)

	m := regexp.MustCompile(`(?m)^\s*name_template: "([^"]+)" # contract$`).FindAllStringSubmatch(cfg, -1)
	if len(m) != 2 {
		t.Fatalf("%s: found %d name_template lines marked \"# contract\", want 2 (archive, checksums)", file, len(m))
	}
	archiveTemplate, checksumsTemplate := m[0][1], m[1][1]
	if checksumsTemplate != ChecksumsName {
		t.Errorf("%s names the checksums file %q, self-update downloads %q", file, checksumsTemplate, ChecksumsName)
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			ext := ".tar.gz"
			if goos == "windows" {
				ext = ".zip"
			}
			built := strings.NewReplacer("{{ .ProjectName }}", appinfo.Name, "{{ .Os }}", goos, "{{ .Arch }}", goarch).Replace(archiveTemplate) + ext
			if want := AssetName(goos, goarch); built != want {
				t.Errorf("%s builds %q, self-update downloads %q", file, built, want)
			}
			// Every platform self-update can ask for must be built.
			mustContain(t, file, cfg, "      - "+goos+"\n")
			mustContain(t, file, cfg, "      - "+goarch+"\n")
		}
	}
	for _, want := range []string{
		"project_name: " + appinfo.Name + "\n",
		"main: ./cmd/" + appinfo.Name + "\n",
		`binary: "{{ .ProjectName }}"`, // BinaryName
		"wrap_in_directory: false",     // the binary is at the root of the archive
		"      - goos: windows\n        formats:\n          - zip\n",
		"    formats:\n      - tar.gz\n",
		"algorithm: sha256",
		"/internal/appinfo.Version=v{{ .Version }}",
		"CGO_ENABLED=0",
		"-trimpath",
	} {
		mustContain(t, file, cfg, want)
	}

	sh := repoFile(t, "install.sh")
	for _, want := range []string{
		`NAME="` + appinfo.Name + `"` + "\n",
		`REPO="` + appinfo.Repo + `"` + "\n",
		`ENV_PREFIX="` + appinfo.EnvPrefix + `"` + "\n",
		`CHECKSUMS="` + ChecksumsName + `"` + "\n",
	} {
		mustContain(t, "install.sh", sh, want)
	}

	// "go install <module>/cmd/<name>@latest", which the updater prints, is
	// only right while the module lives at github.com/<Repo>.
	mustContain(t, "go.mod", repoFile(t, "go.mod"), "module github.com/"+appinfo.Repo+"\n")
	if _, err := os.Stat(filepath.Join("..", "..", "cmd", appinfo.Name, "main.go")); err != nil {
		t.Errorf("the main package is not at cmd/%s: %v", appinfo.Name, err)
	}
	mustContain(t, "Makefile", repoFile(t, "Makefile"), "$(MODULE)/internal/appinfo.Version=$(VERSION)")
}

// fakeCurl stands in for curl: it serves the files of $FAKE_RELEASE by
// base name and logs the requested URLs, so install.sh runs offline.
const fakeCurl = `#!/bin/sh
url=""; dest=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) dest=$2; shift ;;
    http*) url=$1 ;;
  esac
  shift
done
printf '%s\n' "$url" >> "$FAKE_LOG"
[ -f "$FAKE_RELEASE/${url##*/}" ] || { echo "curl: (22) 404" >&2; exit 22; }
cp "$FAKE_RELEASE/${url##*/}" "$dest"
`

// TestInstallScript runs install.sh against archives named and laid out
// the way self-update expects them, which pins the installer to the same
// contract.
func TestInstallScript(t *testing.T) {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("install.sh is only exercised where it can install: Linux and macOS on amd64 and arm64")
	}
	for _, tool := range []string{"sh", "tar", "awk", "uname", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}

	// A release holding every non-Windows archive: whichever the script
	// picks for this machine must be one that the updater would name.
	release := func(t *testing.T) (dir string, assets map[string][]byte) {
		assets = map[string][]byte{}
		for _, goos := range []string{"darwin", "linux"} {
			for _, goarch := range []string{"amd64", "arm64"} {
				assets[AssetName(goos, goarch)] = updatetest.TarGz(t, map[string][]byte{
					"LICENSE":        []byte("MIT"),
					BinaryName(goos): []byte("#!/bin/sh\necho " + goos + "/" + goarch + "\n"),
				})
			}
		}
		assets[ChecksumsName] = updatetest.Checksums(assets)
		return t.TempDir(), assets
	}
	write := func(t *testing.T, dir string, assets map[string][]byte) {
		for name, content := range assets {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run := func(t *testing.T, releaseDir string, env ...string) (installDir, log, output string, err error) {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurl), 0o755); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		installDir = filepath.Join(home, "somewhere", "bin")
		logFile := filepath.Join(t.TempDir(), "log")
		cmd := exec.Command("sh", script)
		cmd.Env = append([]string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
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
	installed := func(t *testing.T, installDir string) []string {
		entries, _ := os.ReadDir(installDir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	t.Run("installs the verified binary", func(t *testing.T) {
		dir, assets := release(t)
		write(t, dir, assets)
		installDir, log, out, err := run(t, dir)
		if err != nil {
			t.Fatalf("install.sh failed: %v\n%s", err, out)
		}
		path := filepath.Join(installDir, appinfo.Name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
		}
		if names := installed(t, installDir); len(names) != 1 {
			t.Errorf("install directory holds %v, want only the binary", names)
		}
		got, err := exec.Command(path).Output()
		if err != nil {
			t.Fatal(err)
		}
		// The script chose an archive for this machine among those named
		// by AssetName.
		if want := runtime.GOOS + "/"; !strings.HasPrefix(string(got), want) {
			t.Errorf("installed the build for %q, want one for %s", strings.TrimSpace(string(got)), want)
		}
		base := "https://github.com/" + appinfo.Repo + "/releases/latest/download/"
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			name, ok := strings.CutPrefix(line, base)
			if _, known := assets[name]; !ok || !known {
				t.Errorf("unexpected download %q", line)
			}
		}
	})

	t.Run("pinned version and PREFIX", func(t *testing.T) {
		dir, assets := release(t)
		write(t, dir, assets)
		prefix := t.TempDir()
		_, log, out, err := run(t, dir, appinfo.EnvName("INSTALL_DIR")+"=", "PREFIX="+prefix, appinfo.EnvName("VERSION")+"=1.2.3")
		if err != nil {
			t.Fatalf("install.sh failed: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(prefix, "bin", appinfo.Name)); err != nil {
			t.Errorf("not installed under $PREFIX/bin: %v\n%s", err, out)
		}
		if want := "https://github.com/" + appinfo.Repo + "/releases/download/v1.2.3/" + ChecksumsName; !strings.Contains(log, want) {
			t.Errorf("downloads = %q, want %q among them", log, want)
		}
	})

	t.Run("refuses a checksum mismatch", func(t *testing.T) {
		dir, assets := release(t)
		for name := range assets {
			if name != ChecksumsName {
				assets[name] = append(assets[name], "tampered"...)
			}
		}
		write(t, dir, assets)
		installDir, _, out, err := run(t, dir)
		if err == nil || !strings.Contains(out, "checksum mismatch") {
			t.Fatalf("install.sh: err = %v, output:\n%s", err, out)
		}
		if names := installed(t, installDir); len(names) != 0 {
			t.Errorf("installed %v despite the mismatch", names)
		}
	})

	t.Run("refuses to install without checksums", func(t *testing.T) {
		for name, mutate := range map[string]func(assets map[string][]byte){
			"missing file":  func(assets map[string][]byte) { delete(assets, ChecksumsName) },
			"missing entry": func(assets map[string][]byte) { assets[ChecksumsName] = []byte("abc  other.tar.gz\n") },
		} {
			t.Run(name, func(t *testing.T) {
				dir, assets := release(t)
				mutate(assets)
				write(t, dir, assets)
				// There is no way to skip verification, not even the
				// predecessor's SKIP_VERIFY switch.
				installDir, _, out, err := run(t, dir, appinfo.EnvName("SKIP_VERIFY")+"=1", appinfo.LegacyEnvPrefix+"SKIP_VERIFY=1")
				if err == nil || !strings.Contains(out, "refusing to install an unverified download") {
					t.Fatalf("install.sh: err = %v, output:\n%s", err, out)
				}
				if names := installed(t, installDir); len(names) != 0 {
					t.Errorf("installed %v without verification", names)
				}
			})
		}
	})

	t.Run("explains a missing release", func(t *testing.T) {
		installDir, _, out, err := run(t, t.TempDir())
		if err == nil || !strings.Contains(out, "could not download") || !strings.Contains(out, "may have no build for") {
			t.Fatalf("install.sh: err = %v, output:\n%s", err, out)
		}
		if names := installed(t, installDir); len(names) != 0 {
			t.Errorf("installed %v", names)
		}
	})
}
