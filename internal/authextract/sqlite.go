package authextract

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/maelvls/slackcli/internal/appinfo"
)

// querySQLiteSnapshot runs fn against a private copy of the SQLite database
// at dbPath.
//
// Browsers keep their databases open, sometimes locked, with recent commits
// still in the write-ahead log. Copying the database and its -wal/-journal
// to a directory only we can read (0700, files 0600), opening that copy
// read-write so SQLite replays the log, and deleting it afterwards is the
// only approach that sees fresh data without ever touching the original.
// The copy holds cookies: it must not outlive this call.
func querySQLiteSnapshot(ctx context.Context, dbPath string, fn func(db *sql.DB) error) (err error) {
	dir, err := os.MkdirTemp("", appinfo.Name+"-sqlite-*")
	if err != nil {
		return fmt.Errorf("creating a private temporary directory: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil && err == nil {
			err = fmt.Errorf("removing the temporary copy of %s: %w", dbPath, rmErr)
		}
	}()
	// The driver treats "?" in a file name as the start of its options.
	if strings.Contains(dir, "?") {
		return fmt.Errorf("temporary directory %q contains '?': set TMPDIR to another directory", dir)
	}

	snapshot := filepath.Join(dir, "db.sqlite")
	if err := copyFilePrivate(dbPath, snapshot); err != nil {
		return fmt.Errorf("copying %s: %w", dbPath, err)
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if err := copyFilePrivate(dbPath+suffix, snapshot+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("copying %s: %w", dbPath+suffix, err)
		}
	}

	db, err := sql.Open("sqlite", snapshot)
	if err != nil {
		return fmt.Errorf("opening a copy of %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close() // before the directory is removed (required on Windows)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("opening a copy of %s: %w", dbPath, err)
	}
	if err := fn(db); err != nil {
		return fmt.Errorf("querying a copy of %s: %w", dbPath, err)
	}
	return nil
}

func copyFilePrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
