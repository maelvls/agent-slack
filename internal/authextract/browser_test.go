package authextract

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testTeamsObject = `{"T1":{"name":"Acme","url":"https://acme.slack.com/","token":"xoxc-acme"}}`

func tabAnswer(t *testing.T, teams, cookie string) string {
	t.Helper()
	out, err := json.Marshal(map[string]string{"teams": teams, "cookie": cookie})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// fakeBrowser serves osascript: the "is running" probe, then the tab
// script.
func fakeBrowser(t *testing.T, running string, tab func() (string, error), keychain map[string]string) (*host, *[]string) {
	t.Helper()
	var calls []string
	h := fakeHost(t, "darwin", func(name string, args ...string) (string, error) {
		switch name {
		case "osascript":
			if len(args) != 2 || args[0] != "-e" {
				t.Errorf("unexpected osascript arguments: %q", args)
			}
			if strings.HasSuffix(args[1], "is running") {
				calls = append(calls, "probe")
				return running, nil
			}
			calls = append(calls, "tab")
			return tab()
		case "security":
			calls = append(calls, "keychain "+args[3])
			if pw, ok := keychain[args[3]]; ok {
				return pw, nil
			}
			return "", &commandError{Name: name, ExitCode: securityItemNotFound}
		}
		t.Errorf("unexpected program %s", name)
		return "", errors.New("unexpected")
	})
	return h, &calls
}

func TestBrowserNotRunningIsNeverLaunched(t *testing.T) {
	for _, running := range []string{"false", ""} {
		h, calls := fakeBrowser(t, running, nil, nil)
		_, err := h.browser(context.Background(), chromeBrowser)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		// The tab script ("tell application …") would start the browser.
		if !slices.Equal(*calls, []string{"probe"}) {
			t.Errorf("calls = %v, want only the probe", *calls)
		}
	}
}

func TestBrowserOnlyOnMacOS(t *testing.T) {
	h := fakeHost(t, "linux", nil)
	if _, err := h.browser(context.Background(), braveBrowser); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestBrowserCookieFromTab(t *testing.T) {
	h, calls := fakeBrowser(t, "true", func() (string, error) {
		return tabAnswer(t, testTeamsObject, testCookieEncoded), nil
	}, nil)
	got, err := h.browser(context.Background(), chromeBrowser)
	if err != nil {
		t.Fatal(err)
	}
	assertTeams(t, got.Teams, Team{URL: "https://acme.slack.com/", Name: "Acme", Token: "xoxc-acme"})
	if got.CookieD != testCookie || got.Source != "Chrome" {
		t.Errorf("CookieD = %q, Source = %q", got.CookieD, got.Source)
	}
	if !slices.Equal(*calls, []string{"probe", "tab"}) {
		t.Errorf("calls = %v: the keychain must not be read when the tab has the cookie", *calls)
	}
}

func TestBrowserCookieFromDatabase(t *testing.T) {
	// The cookie is HttpOnly (invisible to the tab) and the Slack session
	// lives in a non-default profile.
	h, calls := fakeBrowser(t, "true", func() (string, error) {
		return tabAnswer(t, testTeamsObject, ""), nil
	}, map[string]string{"Brave Safe Storage": "brave-pw"})
	dataDir := filepath.Join(h.home, "Library", "Application Support", "BraveSoftware", "Brave-Browser")
	writeChromiumCookies(t, filepath.Join(dataDir, "Default", "Cookies"),
		chromiumCookieRow{host: ".example.com", name: "d", value: "unrelated"})
	db := filepath.Join(dataDir, "Profile 1", "Network", "Cookies")
	writeChromiumCookies(t, db, chromiumCookieRow{
		host: ".slack.com", name: "d",
		encrypted: append([]byte("v10"), encryptCBC(t, []byte(testCookieEncoded), "brave-pw", 1003)...),
	})

	got, err := h.browser(context.Background(), braveBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if got.CookieD != testCookie || got.Paths.Cookies != db || got.Source != "Brave" {
		t.Errorf("CookieD = %q, Paths = %+v, Source = %q", got.CookieD, got.Paths, got.Source)
	}
	if !slices.Equal(*calls, []string{"probe", "tab", "keychain Brave Safe Storage"}) {
		t.Errorf("calls = %v", *calls)
	}
}

func TestBrowserCookieUnreadable(t *testing.T) {
	h, _ := fakeBrowser(t, "true", func() (string, error) {
		return tabAnswer(t, testTeamsObject, ""), nil
	}, nil)
	db := filepath.Join(h.home, "Library", "Application Support", "Google", "Chrome", "Default", "Cookies")
	writeChromiumCookies(t, db, chromiumCookieRow{
		host: ".slack.com", name: "d",
		encrypted: append([]byte("v10"), encryptCBC(t, []byte(testCookieEncoded), "chrome-pw", 1003)...),
	})
	_, err := h.browser(context.Background(), chromeBrowser)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a real error", err)
	}
	if !strings.Contains(err.Error(), "Chrome Safe Storage: not in the keychain") {
		t.Errorf("error does not say why: %v", err)
	}
}

func TestBrowserAppleScriptDisabled(t *testing.T) {
	disabled := func() (string, error) {
		return "", &commandError{Name: "osascript", ExitCode: 1, Stderr: "execution error: " + appleScriptJSDisabledMarker + ". (12)"}
	}
	for b, want := range map[*chromiumBrowser]error{
		&chromeBrowser: ErrChromeAppleScriptDisabled,
		&braveBrowser:  ErrBraveAppleScriptDisabled,
	} {
		h, _ := fakeBrowser(t, "true", disabled, nil)
		if _, err := h.browser(context.Background(), *b); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", b.name, err, want)
		}
	}
}

func TestBrowserFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tab      func() (string, error)
		notFound bool
		contains string
	}{
		{"no Slack tab", func() (string, error) { return "", nil }, true, "no signed-in Slack tab"},
		{"tab without teams", func() (string, error) { return `{"teams":"{}","cookie":""}`, nil }, true, "no signed-in workspace"},
		{"teams of a non-Slack host are refused", func() (string, error) {
			return `{"teams":"{\"T1\":{\"url\":\"https://evil.example/\",\"token\":\"xoxc-evil\"}}","cookie":"xoxd-x"}`, nil
		}, true, "no signed-in workspace"},
		{"garbage answer", func() (string, error) { return "missing value", nil }, false, "unexpected answer"},
		{"automation refused", func() (string, error) {
			return "", &commandError{Name: "osascript", ExitCode: 1, Stderr: "Not authorized to send Apple events to Google Chrome. (-1743)"}
		}, false, "Not authorized"},
		{"timeout", func() (string, error) {
			return "", &commandError{Name: "osascript", ExitCode: -1, Err: context.DeadlineExceeded}
		}, false, "did not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := fakeBrowser(t, "true", tc.tab, nil)
			_, err := h.browser(context.Background(), chromeBrowser)
			if err == nil || errors.Is(err, ErrNotFound) != tc.notFound || !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("err = %v; want notFound=%v mentioning %q", err, tc.notFound, tc.contains)
			}
		})
	}
}

func TestSlackTabScript(t *testing.T) {
	// The JavaScript is embedded in an AppleScript string literal.
	if strings.ContainsAny(slackTabJS, "\"\\\n") {
		t.Error("slackTabJS must not contain double quotes, backslashes or newlines")
	}
	script := slackTabScript("com.google.Chrome")
	for _, want := range []string{`tell application id "com.google.Chrome"`, "location.hostname", "endsWith('.slack.com')"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if got := appleScriptString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("appleScriptString = %s", got)
	}
}
