// Package leveldb is a minimal, read-only reader for LevelDB directories as
// written by Chromium (Local Storage). It parses table files (.ldb/.sst) and
// write-ahead logs (.log) directly, without taking the database lock, so it
// can read the database of a running application.
//
// It deliberately skips the MANIFEST: every table and log file present is
// read, and versions of a key are reconciled with their sequence numbers.
package leveldb

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// maxFileSize bounds how much of one table or log file is read into
// memory. LevelDB tables are ~2 MiB and logs ~4 MiB; anything far beyond
// that is not something this reader should swallow.
const maxFileSize = 256 << 20

// Entry is the live (newest, not deleted) version of a key.
type Entry struct {
	Key   []byte
	Value []byte
	// Seq is the LevelDB sequence number of the write: larger is newer.
	Seq uint64
}

// record is one version of a key found in a table or a log.
type record struct {
	key     []byte
	value   []byte
	seq     uint64
	deleted bool
}

// Scan returns the live entries of the database in dir whose key satisfies
// keep (nil keeps everything), newest first. A key that was overwritten is
// returned once, with its latest value; a deleted key is not returned.
//
// Corrupted files and blocks are skipped: this reader is used on databases
// that another process is writing to.
func Scan(dir string, keep func(key []byte) bool) ([]Entry, error) {
	// A compaction may replace files while we read. Re-read when the set
	// of files changed underneath us, so that we never miss the newest
	// version of a key that moved from a log into a new table.
	const attempts = 3
	var records []record
	for i := 0; ; i++ {
		before, err := listFiles(dir)
		if err != nil {
			return nil, fmt.Errorf("reading LevelDB directory: %w", err)
		}
		records = records[:0]
		for _, name := range before {
			data, err := readBounded(filepath.Join(dir, name))
			if err != nil {
				continue // deleted by a compaction, or unreadable
			}
			if strings.HasSuffix(name, ".log") {
				records = parseLog(data, keep, records)
			} else {
				records = parseTable(data, keep, records)
			}
		}
		after, err := listFiles(dir)
		if err != nil || slices.Equal(before, after) || i == attempts-1 {
			break
		}
	}
	return live(records), nil
}

// listFiles returns the sorted names of the table and log files in dir.
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".ldb", ".sst", ".log":
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("%s is too large (%d bytes)", path, info.Size())
	}
	return os.ReadFile(path)
}

// live keeps the newest version of every key and drops deleted keys.
func live(records []record) []Entry {
	newest := make(map[string]record, len(records))
	for _, r := range records {
		if cur, ok := newest[string(r.key)]; !ok || r.seq >= cur.seq {
			newest[string(r.key)] = r
		}
	}
	out := make([]Entry, 0, len(newest))
	for _, r := range newest {
		if !r.deleted {
			out = append(out, Entry{Key: r.key, Value: r.value, Seq: r.seq})
		}
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if c := cmp.Compare(b.Seq, a.Seq); c != 0 {
			return c
		}
		return slices.Compare(a.Key, b.Key)
	})
	return out
}
