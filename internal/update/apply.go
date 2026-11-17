package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Apply downloads the release with the given tag for this platform,
// verifies it against the release's checksums and atomically replaces the
// running binary with it, keeping the file mode. It returns the path of
// the replaced binary. Nothing is modified unless verification succeeds.
func (u *Updater) Apply(ctx context.Context, tag string) (string, error) {
	if _, err := parseSemver(tag); err != nil {
		return "", fmt.Errorf("refusing to install release %q: not a version", tag)
	}
	exe, err := u.executablePath()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("inspecting the running executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("cannot update %s: not a regular file", exe)
	}

	// Stage the new binary next to the old one: a rename within one
	// directory is atomic, and failing here means failing before the
	// download when the directory is not writable. CreateTemp picks an
	// unpredictable name and creates the file exclusively with mode 0600.
	staged, err := os.CreateTemp(filepath.Dir(exe), "."+filepath.Base(exe)+".new-*")
	if err != nil {
		return "", u.notWritableError(exe, err)
	}
	defer func() {
		staged.Close()
		os.Remove(staged.Name()) // a no-op once it has been renamed into place
	}()

	asset := AssetName(u.GOOS, u.GOARCH)
	sums, err := u.downloadAsset(ctx, tag, ChecksumsName, maxChecksums)
	if err != nil {
		return "", err
	}
	want, err := findChecksum(sums, asset)
	if err != nil {
		return "", err
	}
	archive, err := u.downloadAsset(ctx, tag, asset, maxArchive)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s; nothing was installed", asset, want, got)
	}

	if err := extractBinary(archive, asset, BinaryName(u.GOOS), staged); err != nil {
		return "", err
	}
	if err := staged.Sync(); err != nil {
		return "", fmt.Errorf("writing %s: %w", staged.Name(), err)
	}
	if err := staged.Close(); err != nil {
		return "", fmt.Errorf("writing %s: %w", staged.Name(), err)
	}
	if err := os.Chmod(staged.Name(), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("setting the mode of %s: %w", staged.Name(), err)
	}
	if err := replaceExecutable(u.GOOS, staged.Name(), exe); err != nil {
		return "", u.notWritableError(exe, err)
	}
	return exe, nil
}

// notWritableError explains how to update when the binary cannot be
// replaced in place (system directory, read-only store, …).
func (u *Updater) notWritableError(exe string, err error) error {
	if !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	return fmt.Errorf("cannot update %s in place: %w. Re-run with permission to write to %s, or reinstall into a directory you own with: %s",
		exe, err, filepath.Dir(exe), u.installScriptCommand())
}

// extractBinary copies the file called binName at the root of a release
// archive (tar.gz, or zip for Windows) to w. Entry names are only ever
// compared, never used as paths.
func extractBinary(archive []byte, archiveName, binName string, w io.Writer) error {
	var src io.Reader
	if strings.HasSuffix(archiveName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return fmt.Errorf("reading %s: %w", archiveName, err)
		}
		for _, f := range zr.File {
			if path.Clean(f.Name) != binName || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("reading %s: %w", archiveName, err)
			}
			defer rc.Close()
			src = rc
			break
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return fmt.Errorf("reading %s: %w", archiveName, err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for src == nil {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("reading %s: %w", archiveName, err)
			}
			if hdr.Typeflag == tar.TypeReg && path.Clean(hdr.Name) == binName {
				src = tr
			}
		}
	}
	if src == nil {
		return fmt.Errorf("%s does not contain %s", archiveName, binName)
	}
	n, err := io.Copy(w, io.LimitReader(src, maxBinary+1))
	if err != nil {
		return fmt.Errorf("extracting %s from %s: %w", binName, archiveName, err)
	}
	if n > maxBinary {
		return fmt.Errorf("%s in %s is larger than %d bytes; refusing to install it", binName, archiveName, maxBinary)
	}
	if n == 0 {
		return fmt.Errorf("%s in %s is empty", binName, archiveName)
	}
	return nil
}

// replaceExecutable moves staged over exe. On Unix a rename atomically
// replaces the file even while it runs. Windows refuses to overwrite or
// delete a running executable but allows renaming it, so the old binary is
// moved aside first and put back if the swap fails.
func replaceExecutable(goos, staged, exe string) error {
	if goos != "windows" {
		return os.Rename(staged, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old) // left over from the previous update
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(staged, exe); err != nil {
		if restoreErr := os.Rename(old, exe); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the previous binary failed: %v; it is at %s)", err, restoreErr, old)
		}
		return err
	}
	// Fails while the old image is still running; the next update removes it.
	_ = os.Remove(old)
	return nil
}
