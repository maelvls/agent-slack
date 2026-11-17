package authextract

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/golang/snappy"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// Firefox extracts credentials from Firefox profile storage (macOS and
// Linux). Firefox does not need to be quit.
func Firefox(ctx context.Context) (*Extracted, error) {
	h, err := localHost()
	if err != nil {
		return nil, err
	}
	return h.firefox(ctx)
}

// firefoxSlackOrigin is the directory name Firefox gives to the storage of
// https://app.slack.com. A container tab appends its origin attributes,
// e.g. "https+++app.slack.com^userContextId=2".
const firefoxSlackOrigin = "https+++app.slack.com"

// firefox walks the profiles, default first, and returns the first Slack
// session whose teams and cookie both come from the same profile (and the
// same container).
func (h *host) firefox(ctx context.Context) (*Extracted, error) {
	profiles, err := h.firefoxProfiles()
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, notFoundf("no Firefox profile found")
	}
	var errs []error
	for _, profile := range profiles {
		extracted, err := firefoxFromProfile(ctx, profile)
		if err == nil {
			return extracted, nil
		}
		if !errors.Is(err, ErrNotFound) {
			errs = append(errs, fmt.Errorf("profile %s: %w", filepath.Base(profile.Path), err))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("Firefox: %w", errors.Join(errs...))
	}
	return nil, notFoundf("Firefox: none of the %d profile(s) is signed in to Slack (open https://app.slack.com in Firefox first)", len(profiles))
}

func firefoxFromProfile(ctx context.Context, profile firefoxProfile) (*Extracted, error) {
	storage := filepath.Join(profile.Path, "storage", "default")
	entries, err := os.ReadDir(storage)
	if err != nil {
		return nil, notFoundf("no site storage in %s", profile.Path)
	}
	var origins []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), firefoxSlackOrigin) {
			origins = append(origins, e.Name())
		}
	}
	slices.Sort(origins) // the plain origin sorts before container variants

	cookiesDB := filepath.Join(profile.Path, "cookies.sqlite")
	var errs []error
	for _, origin := range origins {
		localStorageDB := filepath.Join(storage, origin, "ls", "data.sqlite")
		if !exists(localStorageDB) {
			continue
		}
		teams, err := firefoxTeams(ctx, localStorageDB)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(teams) == 0 {
			continue
		}
		originAttributes := strings.TrimPrefix(origin, firefoxSlackOrigin)
		cookie, err := firefoxCookieD(ctx, cookiesDB, originAttributes)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		source := "Firefox profile " + filepath.Base(profile.Path)
		if profile.Name != "" {
			source = "Firefox profile " + profile.Name
		}
		return &Extracted{
			CookieD: cookie,
			Teams:   teams,
			Source:  source,
			Paths:   SourcePaths{Profile: profile.Path, Cookies: cookiesDB, LocalStorage: localStorageDB},
		}, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, notFoundf("no Slack session in %s", profile.Path)
}

// firefoxTeams reads the signed-in teams from a Firefox local storage
// database.
func firefoxTeams(ctx context.Context, dbPath string) ([]Team, error) {
	type row struct {
		value                   []byte
		compression, conversion int64
	}
	var rows []row
	err := querySQLiteSnapshot(ctx, dbPath, func(db *sql.DB) error {
		// Newest config format first.
		res, err := db.QueryContext(ctx, `select value, compression_type, conversion_type from data
			where key in ('localConfig_v2', 'localConfig_v3') order by key desc`)
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var r row
			if err := res.Scan(&r.value, &r.compression, &r.conversion); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return res.Err()
	})
	if err != nil {
		return nil, err
	}
	var parseErr error
	for _, r := range rows {
		text, err := decodeFirefoxValue(r.value, r.compression, r.conversion)
		if err != nil {
			parseErr = err
			continue
		}
		teams, err := parseTeams(text)
		if err != nil {
			parseErr = err
			continue
		}
		if len(teams) > 0 {
			return teams, nil
		}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parsing the Slack localConfig in %s: %w", dbPath, parseErr)
	}
	return nil, nil
}

// Firefox local storage ("LSNG") value encodings, from dom/localstorage.
const (
	firefoxCompressionSnappy = 1
	firefoxConversionUTF8    = 1 // otherwise the value is raw UTF-16
)

// decodeFirefoxValue undoes the compression and text conversion Firefox
// applied to a local storage value. Large values such as Slack's
// localConfig are snappy-compressed.
func decodeFirefoxValue(value []byte, compression, conversion int64) (string, error) {
	if compression == firefoxCompressionSnappy {
		n, err := snappy.DecodedLen(value)
		if err != nil || n > 64<<20 {
			return "", errors.New("invalid snappy-compressed local storage value")
		}
		if value, err = snappy.Decode(nil, value); err != nil {
			return "", fmt.Errorf("decompressing a local storage value: %w", err)
		}
	}
	if conversion == firefoxConversionUTF8 || (utf8.Valid(value) && !slices.Contains(value, 0)) {
		return string(value), nil
	}
	return decodeUTF16LE(value), nil
}

// firefoxCookieD reads the xoxd cookie of the given container (origin
// attributes; "" outside containers) from cookies.sqlite.
func firefoxCookieD(ctx context.Context, dbPath, originAttributes string) (string, error) {
	if !exists(dbPath) {
		return "", notFoundf("no cookie database at %s", dbPath)
	}
	var cookie string
	err := querySQLiteSnapshot(ctx, dbPath, func(db *sql.DB) error {
		// The host is matched exactly or as a subdomain, never as a bare
		// substring (which would accept "slack.com.example.org").
		res, err := db.QueryContext(ctx, `select value from moz_cookies
			where name = 'd' and originAttributes = ?
			  and (host in ('.slack.com', 'slack.com') or host like '%.slack.com')
			order by host = '.slack.com' desc, lastAccessed desc`, originAttributes)
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var value sql.NullString
			if err := res.Scan(&value); err != nil {
				return err
			}
			if strings.HasPrefix(value.String, "xoxd-") {
				cookie = slackapi.DecodeCookieD(value.String)
				return nil
			}
		}
		return res.Err()
	})
	if err != nil {
		return "", err
	}
	if cookie == "" {
		return "", notFoundf("no Slack \"d\" cookie in %s", dbPath)
	}
	return cookie, nil
}
