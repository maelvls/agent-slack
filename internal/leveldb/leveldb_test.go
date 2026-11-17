package leveldb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang/snappy"
)

// write is one write to put in a test table or log.
type write struct {
	key, value string
	seq        uint64
	deleted    bool
}

func put(key, value string, seq uint64) write { return write{key: key, value: value, seq: seq} }
func del(key string, seq uint64) write        { return write{key: key, seq: seq, deleted: true} }

func internalKey(w write) []byte {
	kind := uint64(typeValue)
	if w.deleted {
		kind = typeDeletion
	}
	return binary.LittleEndian.AppendUint64([]byte(w.key), w.seq<<8|kind)
}

// buildBlock encodes key/value pairs as a LevelDB block with prefix
// compression and a single restart point, followed by the block trailer.
func buildBlock(keys, values [][]byte, compress bool) []byte {
	var body, prev []byte
	for i, key := range keys {
		shared := 0
		for shared < len(prev) && shared < len(key) && prev[shared] == key[shared] {
			shared++
		}
		body = binary.AppendUvarint(body, uint64(shared))
		body = binary.AppendUvarint(body, uint64(len(key)-shared))
		body = binary.AppendUvarint(body, uint64(len(values[i])))
		body = append(body, key[shared:]...)
		body = append(body, values[i]...)
		prev = key
	}
	body = binary.LittleEndian.AppendUint32(body, 0) // restart at offset 0
	body = binary.LittleEndian.AppendUint32(body, 1) // one restart point
	kind := byte(compressionNone)
	if compress {
		body = snappy.Encode(nil, body)
		kind = compressionSnappy
	}
	return append(body, kind, 0, 0, 0, 0) // type + CRC placeholder
}

func appendHandle(b []byte, offset, size int) []byte {
	b = binary.AppendUvarint(b, uint64(offset))
	return binary.AppendUvarint(b, uint64(size))
}

// buildTable builds a table file with one data block per group of writes.
func buildTable(compress bool, blocks ...[]write) []byte {
	var file []byte
	var indexKeys, indexValues [][]byte
	for _, writes := range blocks {
		var keys, values [][]byte
		for _, w := range writes {
			keys = append(keys, internalKey(w))
			values = append(values, []byte(w.value))
		}
		block := buildBlock(keys, values, compress)
		indexKeys = append(indexKeys, keys[len(keys)-1])
		indexValues = append(indexValues, appendHandle(nil, len(file), len(block)-blockTrailerSize))
		file = append(file, block...)
	}
	index := buildBlock(indexKeys, indexValues, compress)
	indexOffset := len(file)
	file = append(file, index...)
	meta := buildBlock(nil, nil, false)
	metaOffset := len(file)
	file = append(file, meta...)

	footer := appendHandle(nil, metaOffset, len(meta)-blockTrailerSize)
	footer = appendHandle(footer, indexOffset, len(index)-blockTrailerSize)
	footer = append(footer, make([]byte, footerSize-len(tableMagic)-len(footer))...)
	footer = append(footer, tableMagic...)
	return append(file, footer...)
}

// buildLog builds a log file with one write batch per group of writes,
// splitting records across 32 KiB blocks as LevelDB does. The sequence
// number of a batch is that of its first write.
func buildLog(batches ...[]write) []byte {
	var file []byte
	emit := func(kind byte, payload []byte) {
		file = append(file, 0, 0, 0, 0) // CRC placeholder
		file = binary.LittleEndian.AppendUint16(file, uint16(len(payload)))
		file = append(file, kind)
		file = append(file, payload...)
	}
	for _, writes := range batches {
		batch := binary.LittleEndian.AppendUint64(nil, writes[0].seq)
		batch = binary.LittleEndian.AppendUint32(batch, uint32(len(writes)))
		for _, w := range writes {
			if w.deleted {
				batch = append(batch, typeDeletion)
				batch = binary.AppendUvarint(batch, uint64(len(w.key)))
				batch = append(batch, w.key...)
				continue
			}
			batch = append(batch, typeValue)
			batch = binary.AppendUvarint(batch, uint64(len(w.key)))
			batch = append(batch, w.key...)
			batch = binary.AppendUvarint(batch, uint64(len(w.value)))
			batch = append(batch, w.value...)
		}
		for first := true; ; first = false {
			room := logBlockSize - len(file)%logBlockSize
			if room < logHeaderSize {
				file = append(file, make([]byte, room)...)
				room = logBlockSize
			}
			room -= logHeaderSize
			if len(batch) <= room {
				kind := byte(logLast)
				if first {
					kind = logFull
				}
				emit(kind, batch)
				break
			}
			kind := byte(logMiddle)
			if first {
				kind = logFirst
			}
			emit(kind, batch[:room])
			batch = batch[room:]
		}
	}
	return file
}

func writeDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func scan(t *testing.T, dir string, keep func([]byte) bool) map[string]string {
	t.Helper()
	entries, err := Scan(dir, keep)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		if _, dup := got[string(e.Key)]; dup {
			t.Errorf("key %q returned twice", e.Key)
		}
		got[string(e.Key)] = string(e.Value)
	}
	return got
}

func assertEntries(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(got), keysOf(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("entry %q = %q, want %q", k, truncate(got[k]), truncate(v))
		}
	}
}

func keysOf(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

func TestScanMissingDirectory(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "nope"), nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestScanEmptyDirectory(t *testing.T) {
	assertEntries(t, scan(t, t.TempDir(), nil), map[string]string{})
}

func TestScanTable(t *testing.T) {
	for _, name := range []string{"000001.ldb", "000001.sst"} {
		for _, compress := range []bool{false, true} {
			dir := writeDir(t, map[string][]byte{
				name: buildTable(compress,
					[]write{put("key1", "value1", 1), put("key2", "value2", 2)},
					[]write{put("key3", "value3", 3)}),
			})
			assertEntries(t, scan(t, dir, nil), map[string]string{
				"key1": "value1", "key2": "value2", "key3": "value3",
			})
		}
	}
}

func TestScanLog(t *testing.T) {
	dir := writeDir(t, map[string][]byte{
		"000001.log": buildLog(
			[]write{put("logkey1", "logvalue1", 1), put("logkey2", "logvalue2", 2)},
			[]write{put("logkey3", "logvalue3", 3)}),
	})
	assertEntries(t, scan(t, dir, nil), map[string]string{
		"logkey1": "logvalue1", "logkey2": "logvalue2", "logkey3": "logvalue3",
	})
}

func TestScanLogRecordSpanningBlocks(t *testing.T) {
	big := strings.Repeat("0123456789", 10_000) // 100 kB: FIRST, MIDDLE…, LAST
	dir := writeDir(t, map[string][]byte{
		"000001.log": buildLog(
			[]write{put("small", "v", 1)},
			[]write{put("big", big, 2)},
			[]write{put("after", "w", 3)}),
	})
	assertEntries(t, scan(t, dir, nil), map[string]string{"small": "v", "big": big, "after": "w"})
}

func TestScanLogIgnoresTruncatedTail(t *testing.T) {
	data := buildLog([]write{put("a", "1", 1)}, []write{put("b", "2", 2)})
	dir := writeDir(t, map[string][]byte{"000001.log": data[:len(data)-1]})
	assertEntries(t, scan(t, dir, nil), map[string]string{"a": "1"})
}

func TestScanTableAndLog(t *testing.T) {
	dir := writeDir(t, map[string][]byte{
		"000001.ldb": buildTable(false, []write{put("sst_key", "sst_value", 1)}),
		"000002.log": buildLog([]write{put("log_key", "log_value", 2)}),
	})
	assertEntries(t, scan(t, dir, nil), map[string]string{"sst_key": "sst_value", "log_key": "log_value"})
}

func TestScanReturnsNewestVersion(t *testing.T) {
	dir := writeDir(t, map[string][]byte{
		// File names do not say which file is newer: sequence numbers do.
		"000009.ldb": buildTable(true, []write{put("config", "old", 5), put("gone", "stale", 6)}),
		"000003.ldb": buildTable(false, []write{put("config", "older", 2)}),
		"000010.log": buildLog(
			[]write{put("config", "new", 7), del("gone", 8)},
			[]write{put("config", "newest", 9), put("kept", "yes", 10)}),
	})
	entries, err := Scan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	// Newest first.
	if string(entries[0].Key) != "kept" || entries[0].Seq != 10 {
		t.Errorf("entries[0] = %q seq %d, want kept seq 10", entries[0].Key, entries[0].Seq)
	}
	if string(entries[1].Key) != "config" || string(entries[1].Value) != "newest" || entries[1].Seq != 9 {
		t.Errorf("entries[1] = %q=%q seq %d, want config=newest seq 9", entries[1].Key, entries[1].Value, entries[1].Seq)
	}
}

func TestScanDeletionInTableHidesOlderValue(t *testing.T) {
	dir := writeDir(t, map[string][]byte{
		"000001.ldb": buildTable(false, []write{put("k", "v", 1)}),
		"000002.ldb": buildTable(false, []write{del("k", 2)}),
	})
	assertEntries(t, scan(t, dir, nil), map[string]string{})
}

func TestScanFilter(t *testing.T) {
	dir := writeDir(t, map[string][]byte{
		"000001.ldb": buildTable(false, []write{
			put("_https://app.slack.com localConfig_v2", `{"teams":{}}`, 1),
			put("_https://app.slack.com localConfig_v3", `{"teams":{}}`, 2),
			put("_https://app.slack.com otherKey", "other", 3),
		}),
	})
	contains := func(sub string) func([]byte) bool {
		return func(key []byte) bool { return bytes.Contains(key, []byte(sub)) }
	}
	got := scan(t, dir, contains("localConfig_v"))
	if len(got) != 2 {
		t.Fatalf("got %v, want the two localConfig keys", keysOf(got))
	}
	for k := range got {
		if !strings.Contains(k, "localConfig_v") {
			t.Errorf("unexpected key %q", k)
		}
	}
	assertEntries(t, scan(t, dir, contains("nonexistent")), map[string]string{})
}

func TestScanSkipsInvalidFiles(t *testing.T) {
	good := buildTable(false, []write{put("k", "v", 1)})
	// A table whose only data block has an unknown compression type.
	w := put("k", "v", 1)
	dataBlock := buildBlock([][]byte{internalKey(w)}, [][]byte{[]byte(w.value)}, false)
	damaged := bytes.Clone(good)
	damaged[len(dataBlock)-blockTrailerSize] = 7
	dir := writeDir(t, map[string][]byte{
		"000001.ldb": make([]byte, 100), // wrong magic
		"000002.ldb": {},                // empty
		"000003.ldb": make([]byte, 20),  // smaller than a footer
		"000004.ldb": good[:len(good)-1],
		"000005.log": {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 0x00},
		"000006.ldb": damaged,
		"000007.log": append(make([]byte, logHeaderSize-1), 9), // unknown record type, zero length
		"MANIFEST-1": []byte("ignored"),
	})
	assertEntries(t, scan(t, dir, nil), map[string]string{})
}

func FuzzParse(f *testing.F) {
	f.Add(buildTable(true, []write{put("a", "1", 1), put("ab", "2", 2)}))
	f.Add(buildLog([]write{put("a", "1", 1), del("a", 2)}))
	f.Fuzz(func(t *testing.T, data []byte) {
		parseTable(data, nil, nil) // must not panic
		parseLog(data, nil, nil)
	})
}
