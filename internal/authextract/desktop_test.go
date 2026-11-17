package authextract

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsWindowsStoreSlackPackage(t *testing.T) {
	for name, want := range map[string]bool{
		"com.tinyspeck.slackdesktop_8wekyb3d8bbwe": true,
		"91750D7E.Slack_8she8kybcnzg4":             true,
		"com.example.SlackNotes_123":               false,
		"91750D7E.SlackBackup_123":                 false,
	} {
		if got := isWindowsStoreSlackPackage(name); got != want {
			t.Errorf("isWindowsStoreSlackPackage(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDesktopDataDirsWindows(t *testing.T) {
	h := fakeHost(t, "windows", nil)
	local := filepath.Join(h.home, "Local")
	h.getenv = func(k string) string {
		return map[string]string{"APPDATA": filepath.Join(h.home, "Roaming"), "LOCALAPPDATA": local}[k]
	}
	mustWrite(t, filepath.Join(local, "Packages", "91750D7E.Slack_8she8kybcnzg4", "x"), nil)
	mustWrite(t, filepath.Join(local, "Packages", "91750D7E.SlackBackup_123", "x"), nil)
	got, err := h.desktopDataDirs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(h.home, "Roaming", "Slack"),
		filepath.Join(local, "Packages", "91750D7E.Slack_8she8kybcnzg4", "LocalCache", "Roaming", "Slack"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDesktopMacOS(t *testing.T) {
	var lookups []string
	h := fakeHost(t, "darwin", func(name string, args ...string) (string, error) {
		lookups = append(lookups, name+" "+strings.Join(args, " "))
		if name == "security" && slices.Contains(args, "Slack App Store Key") {
			return "s3cret", nil
		}
		return "", &commandError{Name: name, ExitCode: securityItemNotFound}
	})
	// Only the App Store build is installed.
	dir := filepath.Join(h.home, "Library", "Containers", "com.tinyspeck.slackmacgap", "Data", "Library", "Application Support", "Slack")
	writeSlackDesktop(t, dir, "v10", "s3cret", 1003)

	got, err := h.desktop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertTeams(t, got.Teams, wantTestTeams...)
	if got.CookieD != testCookie {
		t.Errorf("CookieD = %q, want the decoded %q", got.CookieD, testCookie)
	}
	if got.Source != "Slack Desktop" || got.Paths.LevelDB != filepath.Join(dir, "Local Storage", "leveldb") || got.Paths.Cookies != filepath.Join(dir, "Network", "Cookies") {
		t.Errorf("unexpected source: %q %+v", got.Source, got.Paths)
	}
	// Keychain items are read one at a time, stopping at the one that
	// works: each read may prompt the user.
	want := []string{
		"security find-generic-password -w -s Slack Safe Storage -a Slack Key",
		"security find-generic-password -w -s Slack Safe Storage -a Slack App Store Key",
	}
	if !slices.Equal(lookups, want) {
		t.Errorf("keychain lookups = %q, want %q", lookups, want)
	}
}

func TestDesktopLinux(t *testing.T) {
	for _, tc := range []struct {
		name, version, password string
		wantLookups             int
	}{
		{"v10 uses the fixed password without touching the keyring", "v10", "peanuts", 0},
		{"v11 uses the keyring", "v11", "from-keyring", 2},
		{"v11 falls back to the empty password", "v11", "", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups := 0
			h := fakeHost(t, "linux", func(name string, args ...string) (string, error) {
				lookups++
				if name == "secret-tool" && slices.Equal(args, []string{"lookup", "application", "Slack"}) {
					return tc.password, nil
				}
				return "", &commandError{Name: name, ExitCode: 1}
			})
			writeSlackDesktop(t, filepath.Join(h.home, ".config", "Slack"), tc.version, tc.password, 1)
			got, err := h.desktop(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertTeams(t, got.Teams, wantTestTeams...)
			if got.CookieD != testCookie {
				t.Errorf("CookieD = %q, want %q", got.CookieD, testCookie)
			}
			if lookups != tc.wantLookups {
				t.Errorf("%d keyring lookups, want %d", lookups, tc.wantLookups)
			}
		})
	}
}

func TestDesktopNotInstalled(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows", "plan9"} {
		h := fakeHost(t, goos, nil)
		_, err := h.desktop(context.Background())
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", goos, err)
		}
	}
}

func TestDesktopSignedOut(t *testing.T) {
	h := fakeHost(t, "darwin", nil)
	dir := filepath.Join(h.home, "Library", "Application Support", "Slack", "Local Storage", "leveldb")
	writeLevelDBLog(t, dir, [2][]byte{chromiumKey("https://app.slack.com", "localConfig_v2"), chromiumLatin1(`{"teams":{}}`)})
	_, err := h.desktop(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDesktopUndecryptableCookieIsNotNotFound(t *testing.T) {
	h := fakeHost(t, "darwin", func(name string, args ...string) (string, error) {
		if slices.Contains(args, "Slack Key") {
			return "wrong-password", nil
		}
		return "", &commandError{Name: name, ExitCode: securityItemNotFound}
	})
	writeSlackDesktop(t, filepath.Join(h.home, "Library", "Application Support", "Slack"), "v10", "s3cret", 1003)
	_, err := h.desktop(context.Background())
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a real error", err)
	}
	for _, want := range []string{"could not decrypt", "Slack Safe Storage/Slack App Store Key: not in the keychain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestDesktopKeychainTimeoutIsExplained(t *testing.T) {
	h := fakeHost(t, "darwin", func(name string, args ...string) (string, error) {
		return "", &commandError{Name: name, ExitCode: -1, Err: context.DeadlineExceeded}
	})
	writeSlackDesktop(t, filepath.Join(h.home, "Library", "Application Support", "Slack"), "v10", "s3cret", 1003)
	_, err := h.desktop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SLACKCLI_KEYCHAIN_TIMEOUT_MS") {
		t.Fatalf("err = %v, want a hint about the keychain timeout", err)
	}
}

func TestSlackLocalConfigKey(t *testing.T) {
	for key, want := range map[string]bool{
		"_https://app.slack.com\x00\x01localConfig_v2":     true,
		"_https://app.slack.com\x00\x01localConfig_v3":     true,
		"_https://app.slack-gov.com\x00\x01localConfig_v2": true,
		"_https://app.slack.com\x00\x01localConfig_v2x":    false,
		"_https://app.slack.com\x00\x01otherKey":           false,
		"_https://evil.example\x00\x01localConfig_v2":      false,
		"_https://notslack.com\x00\x01localConfig_v2":      false,
		"_http://app.slack.com\x00\x01localConfig_v2":      false,
		"META:https://app.slack.com":                       false,
		"localConfig_v2":                                   false,
		"":                                                 false,
	} {
		if got := slackLocalConfigKey([]byte(key)); got != want {
			t.Errorf("slackLocalConfigKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestTeamsFromChromiumLocalStorageUTF16(t *testing.T) {
	dir := t.TempDir()
	writeLevelDBLog(t, dir,
		[2][]byte{chromiumKey("https://evil.example", "localConfig_v2"), chromiumLatin1(`{"teams":{"T9":{"url":"https://evil.slack.com/","token":"xoxc-evil"}}}`)},
		[2][]byte{chromiumKey("https://app.slack.com", "localConfig_v2"), chromiumUTF16(testLocalConfig)},
	)
	teams, err := teamsFromChromiumLocalStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertTeams(t, teams, wantTestTeams...)
}

func TestDecryptCBC(t *testing.T) {
	enc := encryptCBC(t, []byte("hello "+testCookieEncoded), "pw", 1003)
	plain, err := decryptCBC(enc, "pw", 1003)
	if err != nil {
		t.Fatal(err)
	}
	if cookie, ok := cookieFromPlaintext(plain); !ok || cookie != testCookie {
		t.Errorf("cookie = %q, %v", cookie, ok)
	}
	if _, err := decryptCBC(enc[:len(enc)-1], "pw", 1003); err == nil {
		t.Error("truncated ciphertext accepted")
	}
	if _, err := decryptCBC(nil, "pw", 1003); err == nil {
		t.Error("empty ciphertext accepted")
	}
	if plain, err := decryptCBC(enc, "other", 1003); err == nil {
		if _, ok := cookieFromPlaintext(plain); ok {
			t.Error("wrong password produced a cookie")
		}
	}
}

func TestChromiumCookieIgnoresLookAlikeHosts(t *testing.T) {
	h := fakeHost(t, "linux", func(name string, args ...string) (string, error) {
		return "", &commandError{Name: name, ExitCode: 1}
	})
	db := filepath.Join(t.TempDir(), "Cookies")
	writeChromiumCookies(t, db,
		chromiumCookieRow{host: ".notslack.com", name: "d", value: "xoxd-evil"},
		chromiumCookieRow{host: "slack.com.example.org", name: "d", value: "xoxd-evil"},
	)
	if _, err := h.chromiumCookieD(context.Background(), db, "", h.slackSafeStorage()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// A plaintext cookie (very old Chromium) on a workspace host is used.
	writeChromiumCookies(t, db+"2", chromiumCookieRow{host: "acme.slack.com", name: "d", value: testCookieEncoded})
	got, err := h.chromiumCookieD(context.Background(), db+"2", "", h.slackSafeStorage())
	if err != nil || got != testCookie {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestJoinAttempts(t *testing.T) {
	notFound := notFoundf("no Slack data at /a")
	real := errors.New("could not decrypt")
	if err := joinAttempts("Slack Desktop", []error{notFound, notFound}); !errors.Is(err, ErrNotFound) {
		t.Errorf("all absent: %v, want ErrNotFound", err)
	}
	err := joinAttempts("Slack Desktop", []error{notFound, real})
	if errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "could not decrypt") || !strings.Contains(err.Error(), "/a") {
		t.Errorf("mixed: %v, want a real error mentioning both", err)
	}
}
