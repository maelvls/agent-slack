package authextract

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

const (
	testCookie        = "xoxd-abc+def/ghi=="
	testCookieEncoded = "xoxd-abc%2Bdef%2Fghi%3D%3D"
	testLocalConfig   = `{"teams":{` +
		`"T2":{"id":"T2","name":"Zeta","url":"https://zeta.slack.com/","token":"xoxc-zeta"},` +
		`"T1":{"id":"T1","name":"Acmé","url":"https://acme.slack.com/","token":"xoxc-acme"}}}`
)

// watchTempDir makes temporary files land in a directory that the test
// checks is left empty: copies of cookie databases must not be left behind.
func watchTempDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Cleanup(func() {
		if left, _ := os.ReadDir(tmp); len(left) > 0 {
			t.Errorf("temporary files left behind: %v", left)
		}
	})
}

// fakeHost returns a host rooted in a temp dir, whose helper programs are
// served by run.
func fakeHost(t *testing.T, goos string, run func(name string, args ...string) (string, error)) *host {
	t.Helper()
	watchTempDir(t)
	return &host{
		goos:   goos,
		home:   t.TempDir(),
		getenv: func(string) string { return "" },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			if run == nil {
				t.Errorf("unexpected helper program: %s %v", name, args)
				return "", &commandError{Name: name, ExitCode: 1}
			}
			return run(name, args...)
		},
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLevelDBLog writes a LevelDB write-ahead log holding one batch with
// the given key/value pairs.
func writeLevelDBLog(t *testing.T, dir string, pairs ...[2][]byte) {
	t.Helper()
	batch := binary.LittleEndian.AppendUint64(nil, 1) // sequence
	batch = binary.LittleEndian.AppendUint32(batch, uint32(len(pairs)))
	for _, p := range pairs {
		batch = append(batch, 1) // value record
		batch = binary.AppendUvarint(batch, uint64(len(p[0])))
		batch = append(batch, p[0]...)
		batch = binary.AppendUvarint(batch, uint64(len(p[1])))
		batch = append(batch, p[1]...)
	}
	record := []byte{0, 0, 0, 0} // CRC, not checked
	record = binary.LittleEndian.AppendUint16(record, uint16(len(batch)))
	record = append(record, 1) // full record
	mustWrite(t, filepath.Join(dir, "000003.log"), append(record, batch...))
}

func chromiumKey(origin, key string) []byte {
	return []byte("_" + origin + "\x00\x01" + key)
}

func chromiumLatin1(s string) []byte {
	out := []byte{1}
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}

func chromiumUTF16(s string) []byte {
	out := []byte{0}
	for _, u := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// encryptCBC is the inverse of decryptCBC.
func encryptCBC(t *testing.T, plain []byte, password string, iterations int) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), iterations, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(strings.Repeat(" ", aes.BlockSize))).CryptBlocks(out, padded)
	return out
}

func openForTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func execSQL(t *testing.T, path string, statements ...any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < len(statements); i++ {
		query := statements[i].(string)
		var args []any
		if i+1 < len(statements) {
			if a, ok := statements[i+1].([]any); ok {
				args = a
				i++
			}
		}
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
}

// chromiumCookieRow is a row for writeChromiumCookies.
type chromiumCookieRow struct {
	host, name, value string
	encrypted         []byte
}

func writeChromiumCookies(t *testing.T, path string, rows ...chromiumCookieRow) {
	t.Helper()
	statements := []any{`create table cookies (host_key text, name text, value text, encrypted_value blob)`}
	for _, r := range rows {
		statements = append(statements, `insert into cookies values (?, ?, ?, ?)`, []any{r.host, r.name, r.value, r.encrypted})
	}
	execSQL(t, path, statements...)
}

// writeSlackDesktop creates a Slack Desktop data directory holding
// testLocalConfig and a "d" cookie encrypted with password.
func writeSlackDesktop(t *testing.T, dir, version, password string, iterations int) {
	t.Helper()
	writeLevelDBLog(t, filepath.Join(dir, "Local Storage", "leveldb"),
		[2][]byte{chromiumKey("https://app.slack.com", "localConfig_v2"), chromiumLatin1(testLocalConfig)})
	// Recent Chromium versions prefix the plaintext with a domain hash.
	plain := append(bytes.Repeat([]byte{0xAB}, 32), testCookieEncoded...)
	writeChromiumCookies(t, filepath.Join(dir, "Network", "Cookies"),
		chromiumCookieRow{host: ".slack.com", name: "d", encrypted: append([]byte(version), encryptCBC(t, plain, password, iterations)...)},
		chromiumCookieRow{host: ".slack.com", name: "other", value: "x"},
	)
}

func assertTeams(t *testing.T, got []Team, want ...Team) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d teams, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("team %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

var wantTestTeams = []Team{
	{URL: "https://zeta.slack.com/", Name: "Zeta", Token: "xoxc-zeta"},
	{URL: "https://acme.slack.com/", Name: "Acmé", Token: "xoxc-acme"},
}
