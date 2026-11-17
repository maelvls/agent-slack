package authextract

// The approach is inspired by slacktokens
// (https://github.com/hraftery/slacktokens).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/maelvls/slackcli/internal/leveldb"
)

// Desktop extracts credentials from the Slack desktop app's local data
// (macOS, Linux, Windows). Slack does not need to be quit.
func Desktop(ctx context.Context) (*Extracted, error) {
	h, err := localHost()
	if err != nil {
		return nil, err
	}
	return h.desktop(ctx)
}

// windowsStoreSlackPackagePrefixes are the Microsoft Store package names of
// Slack, without their per-publisher hash suffix.
var windowsStoreSlackPackagePrefixes = []string{"com.tinyspeck.slackdesktop_", "91750D7E.Slack_"}

func isWindowsStoreSlackPackage(name string) bool {
	for _, prefix := range windowsStoreSlackPackagePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// desktopDataDirs lists where Slack Desktop may keep its data on this OS.
func (h *host) desktopDataDirs() ([]string, error) {
	switch h.goos {
	case "darwin":
		return []string{
			// Direct download (Electron).
			filepath.Join(h.home, "Library", "Application Support", "Slack"),
			// Mac App Store (sandboxed container).
			filepath.Join(h.home, "Library", "Containers", "com.tinyspeck.slackmacgap", "Data", "Library", "Application Support", "Slack"),
		}, nil
	case "linux":
		return []string{
			filepath.Join(h.home, ".var", "app", "com.slack.Slack", "config", "Slack"), // Flatpak
			filepath.Join(h.home, ".config", "Slack"),
		}, nil
	case "windows":
		appData := h.getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(h.home, "AppData", "Roaming")
		}
		dirs := []string{filepath.Join(appData, "Slack")}
		localAppData := h.getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(h.home, "AppData", "Local")
		}
		packages := filepath.Join(localAppData, "Packages")
		entries, _ := os.ReadDir(packages) // absent without Store apps
		for _, e := range entries {
			if isWindowsStoreSlackPackage(e.Name()) {
				dirs = append(dirs, filepath.Join(packages, e.Name(), "LocalCache", "Roaming", "Slack"))
			}
		}
		return dirs, nil
	}
	return nil, notFoundf("Slack Desktop extraction is not supported on %s", h.goos)
}

func (h *host) desktop(ctx context.Context) (*Extracted, error) {
	dirs, err := h.desktopDataDirs()
	if err != nil {
		return nil, err
	}
	keys := h.slackSafeStorage()
	var errs []error
	for _, dir := range dirs {
		extracted, err := h.desktopFromDir(ctx, dir, keys)
		if err == nil {
			return extracted, nil
		}
		errs = append(errs, err)
	}
	return nil, joinAttempts("Slack Desktop", errs)
}

// desktopFromDir extracts the teams and the cookie of one Slack data
// directory; both must come from the same installation.
func (h *host) desktopFromDir(ctx context.Context, dir string, keys *safeStorage) (*Extracted, error) {
	levelDBDir := filepath.Join(dir, "Local Storage", "leveldb")
	teams, err := teamsFromChromiumLocalStorage(levelDBDir)
	if err != nil {
		return nil, err
	}
	// Chromium moved the cookie database into "Network" in 2021.
	cookiesDB := filepath.Join(dir, "Network", "Cookies")
	if !exists(cookiesDB) {
		cookiesDB = filepath.Join(dir, "Cookies")
	}
	cookie, err := h.chromiumCookieD(ctx, cookiesDB, dir, keys)
	if err != nil {
		return nil, err
	}
	return &Extracted{
		CookieD: cookie,
		Teams:   teams,
		Source:  "Slack Desktop",
		Paths:   SourcePaths{LevelDB: levelDBDir, Cookies: cookiesDB},
	}, nil
}

// slackLocalConfigKey reports whether a Chromium Local Storage key is a
// Slack origin's localConfig. Keys look like
// "_https://app.slack.com\x00\x01localConfig_v2".
func slackLocalConfigKey(key []byte) bool {
	origin, name, ok := bytes.Cut(key, []byte{0})
	if !ok || len(origin) == 0 || origin[0] != '_' || len(name) == 0 || slackOrigin(string(origin[1:])) == "" {
		return false
	}
	for _, k := range localConfigKeys {
		if string(name[1:]) == k {
			return true
		}
	}
	return false
}

// teamsFromChromiumLocalStorage reads the signed-in teams from a Chromium
// Local Storage LevelDB. The database is read in place and in memory: no
// copy of it (it holds tokens) is ever written to disk.
func teamsFromChromiumLocalStorage(levelDBDir string) ([]Team, error) {
	entries, err := leveldb.Scan(levelDBDir, slackLocalConfigKey)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notFoundf("no Slack data at %s", levelDBDir)
	}
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, notFoundf("%s holds no Slack localConfig (not signed in?)", levelDBDir)
	}
	// Entries come newest first: use the most recently written config that
	// lists signed-in teams.
	var parseErr error
	for _, e := range entries {
		teams, err := parseTeams(decodeChromiumString(e.Value))
		if err != nil {
			parseErr = err
			continue
		}
		if len(teams) > 0 {
			return teams, nil
		}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parsing the Slack localConfig in %s: %w", levelDBDir, parseErr)
	}
	return nil, notFoundf("the Slack localConfig in %s lists no signed-in workspace", levelDBDir)
}

// decodeChromiumString decodes a Chromium Local Storage value: one format
// byte (0 = UTF-16LE, 1 = Latin-1) followed by the string.
func decodeChromiumString(v []byte) string {
	if len(v) == 0 {
		return ""
	}
	switch v[0] {
	case 0:
		return decodeUTF16LE(v[1:])
	case 1:
		runes := make([]rune, len(v)-1)
		for i, b := range v[1:] {
			runes[i] = rune(b)
		}
		return string(runes)
	}
	return string(v)
}

func decodeUTF16LE(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}
