package authextract

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/golang/snappy"
)

func TestParseProfilesINI(t *testing.T) {
	ini := "[Install4F96D1932A9F858E]\r\nDefault=Profiles/abcd.default-release\r\nLocked=1\r\n\r\n" +
		"; comment\n# another\n" +
		"[Profile1]\nName=default\nIsRelative=1\nPath=Profiles/wxyz.default\nDefault=1\n\n" +
		"[Profile0]\nName = default-release\nIsRelative=1\nPath=Profiles/abcd.default-release\n\n" +
		"[Profile2]\nName=elsewhere\nIsRelative=0\nPath=/mnt/profiles/elsewhere\n\n" +
		"[Profile3]\nName=no-path\n\n" +
		"[General]\nStartWithLastProfile=1\nVersion=2\n"
	got := parseProfilesINI(ini, "/base")
	want := []firefoxProfile{
		{Name: "default", Path: filepath.Join("/base", "Profiles", "wxyz.default"), IsDefault: true},
		{Name: "default-release", Path: filepath.Join("/base", "Profiles", "abcd.default-release"), IsDefault: true},
		{Name: "elsewhere", Path: "/mnt/profiles/elsewhere"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profile %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// firefoxValue encodes a local storage value the way Firefox stores it.
type firefoxValue struct {
	key, text   string
	snappy, u16 bool
}

func writeFirefoxLocalStorage(t *testing.T, profile, origin string, values ...firefoxValue) string {
	t.Helper()
	path := filepath.Join(profile, "storage", "default", origin, "ls", "data.sqlite")
	statements := []any{`create table data (key text primary key, utf16_length integer not null default 0,
		conversion_type integer not null default 0, compression_type integer not null default 0,
		last_access_time integer not null default 0, value blob not null)`}
	for _, v := range values {
		blob, conversion, compression := []byte(v.text), 1, 0
		if v.u16 {
			blob, conversion = nil, 0
			for _, u := range utf16.Encode([]rune(v.text)) {
				blob = binary.LittleEndian.AppendUint16(blob, u)
			}
		}
		if v.snappy {
			blob, compression = snappy.Encode(nil, blob), 1
		}
		statements = append(statements,
			`insert into data (key, conversion_type, compression_type, value) values (?, ?, ?, ?)`,
			[]any{v.key, conversion, compression, blob})
	}
	execSQL(t, path, statements...)
	return path
}

type firefoxCookie struct{ host, name, value, originAttributes string }

func writeFirefoxCookies(t *testing.T, profile string, cookies ...firefoxCookie) string {
	t.Helper()
	path := filepath.Join(profile, "cookies.sqlite")
	statements := []any{`create table moz_cookies (id integer primary key, originAttributes text not null default '',
		name text, value text, host text, lastAccessed integer)`}
	for i, c := range cookies {
		statements = append(statements,
			`insert into moz_cookies (originAttributes, name, value, host, lastAccessed) values (?, ?, ?, ?, ?)`,
			[]any{c.originAttributes, c.name, c.value, c.host, i})
	}
	execSQL(t, path, statements...)
	return path
}

func firefoxHost(t *testing.T, ini string) (h *host, baseDir string) {
	t.Helper()
	h = fakeHost(t, "darwin", nil)
	baseDir = filepath.Join(h.home, "Library", "Application Support", "Firefox")
	mustWrite(t, filepath.Join(baseDir, "profiles.ini"), []byte(ini))
	return h, baseDir
}

func TestFirefox(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value firefoxValue
	}{
		{"snappy-compressed UTF-8", firefoxValue{key: "localConfig_v2", text: testLocalConfig, snappy: true}},
		{"snappy-compressed UTF-16", firefoxValue{key: "localConfig_v2", text: testLocalConfig, snappy: true, u16: true}},
		{"plain UTF-8", firefoxValue{key: "localConfig_v3", text: testLocalConfig}},
		{"plain UTF-16", firefoxValue{key: "localConfig_v2", text: testLocalConfig, u16: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, baseDir := firefoxHost(t, "[Profile0]\nName=work\nIsRelative=1\nPath=Profiles/abcd.work\nDefault=1\n")
			// A stale, signed-out profile that profiles.ini does not list.
			if err := os.MkdirAll(filepath.Join(baseDir, "Profiles", "0000.old", "storage", "default"), 0o700); err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(baseDir, "Profiles", "abcd.work")
			lsPath := writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin, tc.value,
				firefoxValue{key: "unrelated", text: "x"})
			cookiesPath := writeFirefoxCookies(t, profile,
				firefoxCookie{host: ".notslack.com", name: "d", value: "xoxd-lookalike"},
				firefoxCookie{host: "slack.com.example.org", name: "d", value: "xoxd-lookalike"},
				firefoxCookie{host: ".slack.com", name: "d", value: "xoxd-container", originAttributes: "^userContextId=2"},
				firefoxCookie{host: ".slack.com", name: "other", value: "xoxd-other"},
				firefoxCookie{host: ".slack.com", name: "d", value: testCookieEncoded},
			)

			got, err := h.firefox(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertTeams(t, got.Teams, wantTestTeams...)
			if got.CookieD != testCookie {
				t.Errorf("CookieD = %q, want %q", got.CookieD, testCookie)
			}
			wantPaths := SourcePaths{Profile: profile, Cookies: cookiesPath, LocalStorage: lsPath}
			if got.Source != "Firefox profile work" || got.Paths != wantPaths {
				t.Errorf("Source = %q, Paths = %+v", got.Source, got.Paths)
			}
		})
	}
}

func TestFirefoxContainerTab(t *testing.T) {
	h, baseDir := firefoxHost(t, "")
	// No profiles.ini entry: the profile is found by scanning Profiles/.
	profile := filepath.Join(baseDir, "Profiles", "abcd.default")
	writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin+"^userContextId=2",
		firefoxValue{key: "localConfig_v2", text: testLocalConfig, snappy: true})
	writeFirefoxCookies(t, profile,
		firefoxCookie{host: ".slack.com", name: "d", value: "xoxd-other-container", originAttributes: "^userContextId=3"},
		firefoxCookie{host: ".slack.com", name: "d", value: "xoxd-container", originAttributes: "^userContextId=2"},
	)
	got, err := h.firefox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The cookie must come from the container the tokens came from.
	if got.CookieD != "xoxd-container" || got.Source != "Firefox profile abcd.default" {
		t.Errorf("CookieD = %q, Source = %q", got.CookieD, got.Source)
	}
}

func TestFirefoxPrefersDefaultProfile(t *testing.T) {
	h, baseDir := firefoxHost(t,
		"[Profile0]\nName=first\nIsRelative=1\nPath=Profiles/first\n\n[Profile1]\nName=main\nIsRelative=1\nPath=Profiles/main\nDefault=1\n")
	for _, name := range []string{"first", "main"} {
		profile := filepath.Join(baseDir, "Profiles", name)
		writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin, firefoxValue{key: "localConfig_v2", text: testLocalConfig})
		writeFirefoxCookies(t, profile, firefoxCookie{host: ".slack.com", name: "d", value: "xoxd-" + name})
	}
	got, err := h.firefox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CookieD != "xoxd-main" {
		t.Errorf("CookieD = %q, want the default profile's", got.CookieD)
	}
}

func TestFirefoxNotFound(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		for _, goos := range []string{"darwin", "linux", "windows"} {
			h := fakeHost(t, goos, nil)
			if _, err := h.firefox(context.Background()); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", goos, err)
			}
		}
	})
	t.Run("signed out", func(t *testing.T) {
		h, baseDir := firefoxHost(t, "")
		profile := filepath.Join(baseDir, "Profiles", "abcd.default")
		writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin, firefoxValue{key: "localConfig_v2", text: `{"teams":{}}`})
		writeFirefoxCookies(t, profile)
		if _, err := h.firefox(context.Background()); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("tokens without cookie", func(t *testing.T) {
		h, baseDir := firefoxHost(t, "")
		profile := filepath.Join(baseDir, "Profiles", "abcd.default")
		writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin, firefoxValue{key: "localConfig_v2", text: testLocalConfig})
		writeFirefoxCookies(t, profile)
		if _, err := h.firefox(context.Background()); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestFirefoxCorruptedDatabaseIsReported(t *testing.T) {
	h, baseDir := firefoxHost(t, "")
	profile := filepath.Join(baseDir, "Profiles", "abcd.default")
	mustWrite(t, filepath.Join(profile, "storage", "default", firefoxSlackOrigin, "ls", "data.sqlite"), []byte("not a database"))
	_, err := h.firefox(context.Background())
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "abcd.default") {
		t.Errorf("err = %v, want a real error naming the profile", err)
	}
}

func TestFirefoxLinuxSnapProfile(t *testing.T) {
	h := fakeHost(t, "linux", nil)
	baseDir := filepath.Join(h.home, "snap", "firefox", "common", ".mozilla", "firefox")
	mustWrite(t, filepath.Join(baseDir, "profiles.ini"), []byte("[Profile0]\nName=default\nIsRelative=1\nPath=abcd.default\n"))
	profile := filepath.Join(baseDir, "abcd.default")
	writeFirefoxLocalStorage(t, profile, firefoxSlackOrigin, firefoxValue{key: "localConfig_v2", text: testLocalConfig, snappy: true})
	writeFirefoxCookies(t, profile, firefoxCookie{host: ".slack.com", name: "d", value: testCookieEncoded})
	got, err := h.firefox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CookieD != testCookie || len(got.Teams) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestSQLiteSnapshotSeesWriteAheadLog(t *testing.T) {
	watchTempDir(t)
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	// Keep a connection open, like a running browser: the row only exists
	// in the -wal file.
	execSQL(t, path, `pragma journal_mode = wal`)
	db := openForTest(t, path)
	for _, q := range []string{
		`pragma wal_autocheckpoint = 0`,
		`create table moz_cookies (originAttributes text, name text, value text, host text, lastAccessed integer)`,
		`insert into moz_cookies values ('', 'd', 'xoxd-fresh', '.slack.com', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if !exists(path + "-wal") {
		t.Fatal("fixture has no write-ahead log")
	}
	got, err := firefoxCookieD(context.Background(), path, "")
	if err != nil || got != "xoxd-fresh" {
		t.Fatalf("got %q, %v", got, err)
	}
}
