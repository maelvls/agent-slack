package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/update/updatetest"
)

func TestApplyReplacesBinary(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newFixture(t)
			f.u.GOOS, f.u.GOARCH = goos, "arm64"
			if runtime.GOOS != "windows" {
				// An unusual mode shows that the old one is carried over.
				if err := os.Chmod(f.exe, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Stat(f.exe)
			if err != nil {
				t.Fatal(err)
			}
			f.publish(t, "v1.2.0", "new binary")

			path, err := f.u.Apply(context.Background(), "v1.2.0")
			if err != nil {
				t.Fatal(err)
			}
			if path != f.exe {
				t.Errorf("Apply() = %q, want %q", path, f.exe)
			}
			if got := f.exeContent(t); got != "new binary" {
				t.Errorf("binary content = %q, want the new binary", got)
			}
			after, err := os.Stat(f.exe)
			if err != nil {
				t.Fatal(err)
			}
			if after.Mode() != before.Mode() {
				t.Errorf("mode = %v, want %v", after.Mode(), before.Mode())
			}
			f.assertNoLeftovers(t)

			want := []string{ChecksumsName, AssetName(goos, "arm64")}
			if got := f.srv.Downloads(); !slices.Equal(got, want) {
				t.Errorf("downloads = %v, want %v", got, want)
			}
		})
	}
}

func TestApplyThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newFixture(t)
	f.publish(t, "v1.2.0", "new binary")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f.exe, link); err != nil {
		t.Fatal(err)
	}
	f.u.Executable = func() (string, error) { return link, nil }

	if _, err := f.u.Apply(context.Background(), "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced instead of its target (%v, %v)", fi, err)
	}
	if got := f.exeContent(t); got != "new binary" {
		t.Errorf("binary content = %q, want the new binary", got)
	}
}

// TestApplyRefusesUnverified covers every way a download can fail
// verification: the binary must be left untouched, with no stray files.
func TestApplyRefusesUnverified(t *testing.T) {
	asset := AssetName("linux", "amd64")
	good := func(t *testing.T) []byte {
		return updatetest.TarGz(t, map[string][]byte{BinaryName("linux"): []byte("new binary")})
	}
	tests := []struct {
		name    string
		tag     string
		assets  func(t *testing.T) map[string][]byte
		wantErr string
	}{
		{
			name: "checksum mismatch",
			assets: func(t *testing.T) map[string][]byte {
				sums := updatetest.Checksums(map[string][]byte{asset: []byte("something else")})
				return map[string][]byte{asset: good(t), ChecksumsName: sums}
			},
			wantErr: "checksum mismatch for " + asset,
		},
		{
			name: "no checksums file",
			assets: func(t *testing.T) map[string][]byte {
				return map[string][]byte{asset: good(t)}
			},
			wantErr: "no asset " + ChecksumsName,
		},
		{
			name: "archive missing from the checksums",
			assets: func(t *testing.T) map[string][]byte {
				sums := updatetest.Checksums(map[string][]byte{"other.tar.gz": good(t)})
				return map[string][]byte{asset: good(t), ChecksumsName: sums}
			},
			wantErr: "no entry for " + asset,
		},
		{
			name: "empty checksums file",
			assets: func(t *testing.T) map[string][]byte {
				return map[string][]byte{asset: good(t), ChecksumsName: {}}
			},
			wantErr: "no entry for " + asset,
		},
		{
			name: "no archive for this platform",
			assets: func(t *testing.T) map[string][]byte {
				sums := updatetest.Checksums(map[string][]byte{asset: good(t)})
				return map[string][]byte{ChecksumsName: sums}
			},
			wantErr: "is linux/amd64 a supported platform?",
		},
		{
			name: "archive without the binary",
			assets: func(t *testing.T) map[string][]byte {
				a := map[string][]byte{asset: updatetest.TarGz(t, map[string][]byte{
					"LICENSE":                       []byte("MIT"),
					"nested/" + BinaryName("linux"): []byte("wrong place"),
				})}
				a[ChecksumsName] = updatetest.Checksums(a)
				return a
			},
			wantErr: "does not contain " + BinaryName("linux"),
		},
		{
			name: "archive with an empty binary",
			assets: func(t *testing.T) map[string][]byte {
				a := map[string][]byte{asset: updatetest.TarGz(t, map[string][]byte{BinaryName("linux"): {}})}
				a[ChecksumsName] = updatetest.Checksums(a)
				return a
			},
			wantErr: "is empty",
		},
		{
			name: "archive that is not an archive",
			assets: func(t *testing.T) map[string][]byte {
				a := map[string][]byte{asset: []byte("<html>rate limited</html>")}
				a[ChecksumsName] = updatetest.Checksums(a)
				return a
			},
			wantErr: "reading " + asset,
		},
		{
			name:    "tag that is not a version",
			tag:     "../../../evil",
			assets:  func(t *testing.T) map[string][]byte { return nil },
			wantErr: "not a version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tag := tt.tag
			if tag == "" {
				tag = "v1.2.0"
			}
			f.srv.Publish(tag, tt.assets(t))
			_, err := f.u.Apply(context.Background(), tag)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Apply() error = %v, want %q", err, tt.wantErr)
			}
			if got := f.exeContent(t); got != "old binary" {
				t.Errorf("binary content = %q: it was modified despite the failure", got)
			}
			f.assertNoLeftovers(t)
		})
	}
}

func TestApplyUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	f := newFixture(t)
	f.publish(t, "v1.2.0", "new binary")
	dir := filepath.Dir(f.exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, err := f.u.Apply(context.Background(), "v1.2.0")
	if err == nil {
		t.Fatal("Apply() succeeded in a read-only directory")
	}
	for _, want := range []string{"cannot update " + f.exe, "Re-run with permission to write to", "install.sh | sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if got := f.srv.Downloads(); len(got) != 0 {
		t.Errorf("downloaded %v before finding out the directory is read-only", got)
	}
	if got := f.exeContent(t); got != "old binary" {
		t.Errorf("binary content = %q, want it untouched", got)
	}
}

func TestReplaceExecutableWindowsRollsBack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale file from a previous update must not get in the way.
	if err := os.WriteFile(exe+".old", []byte("older"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The staged file does not exist, so the swap fails after the running
	// binary has been moved aside.
	if err := replaceExecutable("windows", filepath.Join(dir, "missing"), exe); err == nil {
		t.Fatal("replaceExecutable succeeded without a staged file")
	}
	if b, err := os.ReadFile(exe); err != nil || string(b) != "old" {
		t.Errorf("binary after a failed swap = %q, %v; want the old one restored", b, err)
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Errorf("%s.old still exists after the rollback (%v)", exe, err)
	}
}

func TestExtractBinaryIgnoresTraversalNames(t *testing.T) {
	// Entry names are compared, never joined to a path: an entry that
	// merely cleans to the binary name is extracted to the given writer.
	archive := updatetest.TarGz(t, map[string][]byte{
		"../../etc/passwd": []byte("evil"),
		"./tool":           []byte("good"),
	})
	var out bytes.Buffer
	if err := extractBinary(archive, "a.tar.gz", "tool", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "good" {
		t.Errorf("extracted %q, want %q", out.String(), "good")
	}
}
