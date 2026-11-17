package authextract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// chromiumBrowser describes a Chromium-based browser that can be scripted
// with AppleScript on macOS.
type chromiumBrowser struct {
	name string
	// bundleID addresses the application without naming it: AppleScript
	// asks the user to locate an application named in a script when it is
	// not installed, but not one referenced by ID.
	bundleID string
	// dataDir is the user data directory, relative to
	// ~/Library/Application Support.
	dataDir string
	// keychain lists where the browser keeps its Safe Storage password.
	keychain []keychainItem
	// errDisabled is returned when the browser refuses to run JavaScript
	// for AppleScript.
	errDisabled error
}

var (
	chromeBrowser = chromiumBrowser{
		name:        "Chrome",
		bundleID:    "com.google.Chrome",
		dataDir:     filepath.Join("Google", "Chrome"),
		keychain:    []keychainItem{{"Chrome Safe Storage", ""}},
		errDisabled: ErrChromeAppleScriptDisabled,
	}
	braveBrowser = chromiumBrowser{
		name:        "Brave",
		bundleID:    "com.brave.Browser",
		dataDir:     filepath.Join("BraveSoftware", "Brave-Browser"),
		keychain:    []keychainItem{{"Brave Safe Storage", ""}, {"Brave Browser Safe Storage", ""}},
		errDisabled: ErrBraveAppleScriptDisabled,
	}
)

// Chrome extracts credentials from a logged-in Slack tab in Google Chrome
// (macOS, via AppleScript). It returns ErrChromeAppleScriptDisabled when
// Chrome refuses AppleScript.
func Chrome(ctx context.Context) (*Extracted, error) {
	h, err := localHost()
	if err != nil {
		return nil, err
	}
	return h.browser(ctx, chromeBrowser)
}

// Brave extracts credentials from a logged-in Slack tab in Brave (macOS).
// It returns ErrBraveAppleScriptDisabled when Brave refuses AppleScript.
func Brave(ctx context.Context) (*Extracted, error) {
	h, err := localHost()
	if err != nil {
		return nil, err
	}
	return h.browser(ctx, braveBrowser)
}

const (
	appleScriptTimeout = 7 * time.Second
	// appleScriptJSDisabledMarker is in the error Chromium raises when
	// View → Developer → Allow JavaScript from Apple Events is off.
	appleScriptJSDisabledMarker = "Executing JavaScript through AppleScript is turned off"
)

// slackTabJS runs in a browser tab and returns "" unless the tab is a
// signed-in Slack page, else {"teams": <JSON text>, "cookie": <d cookie or
// "">}. The hostname check matters: the tab is picked by a loose URL match,
// and a non-Slack page must not be able to hand us "teams" of its choosing.
// It must contain neither double quotes nor backslashes (see
// appleScriptString).
const slackTabJS = `(function(){` +
	`var h=location.hostname;` +
	`if(h!=='slack.com'&&!h.endsWith('.slack.com')&&!h.endsWith('.slack-gov.com'))return '';` +
	`var read=[` +
	`function(){return JSON.stringify(JSON.parse(localStorage.localConfig_v3).teams)},` +
	`function(){return JSON.stringify(JSON.parse(localStorage.localConfig_v2).teams)},` +
	`function(){return JSON.stringify(JSON.parse(localStorage.getItem('reduxPersist:localConfig')).teams)},` +
	`function(){return JSON.stringify(window.boot_data.teams)}];` +
	`var teams='';` +
	`for(var i=0;i<read.length&&!teams;i++){try{var v=read[i]();if(v&&v!=='{}'&&v!=='null')teams=v}catch(e){}}` +
	`if(!teams)return '';` +
	`var c=document.cookie.split('; ').filter(function(c){return c.indexOf('d=')===0})[0]||'';` +
	`return JSON.stringify({teams:teams,cookie:c.slice(2)})` +
	`})()`

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// slackTabScript asks the browser to run slackTabJS in each tab whose URL
// mentions slack.com until one answers.
func slackTabScript(bundleID string) string {
	return `tell application id ` + appleScriptString(bundleID) + `
	repeat with w in windows
		repeat with t in tabs of w
			if URL of t contains "slack.com" then
				set r to execute t javascript ` + appleScriptString(slackTabJS) + `
				if r is not missing value and r is not "" then return r
			end if
		end repeat
	end repeat
	return ""
end tell`
}

func (h *host) osascript(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, appleScriptTimeout)
	defer cancel()
	return h.run(ctx, "osascript", "-e", script)
}

func (h *host) browser(ctx context.Context, b chromiumBrowser) (*Extracted, error) {
	if h.goos != "darwin" {
		return nil, notFoundf("importing from %s is only supported on macOS", b.name)
	}
	// "tell application" would launch the browser: only talk to it when
	// it is already running.
	running, err := h.osascript(ctx, `application id `+appleScriptString(b.bundleID)+` is running`)
	if err != nil || running != "true" {
		return nil, notFoundf("%s is not running", b.name)
	}

	out, err := h.osascript(ctx, slackTabScript(b.bundleID))
	if err != nil {
		if strings.Contains(err.Error(), appleScriptJSDisabledMarker) {
			return nil, b.errDisabled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s did not answer AppleScript within %s (macOS may be asking for permission to control it)", b.name, appleScriptTimeout)
		}
		return nil, fmt.Errorf("asking %s for its Slack tab: %w", b.name, err)
	}
	if out == "" {
		return nil, notFoundf("no signed-in Slack tab is open in %s", b.name)
	}
	var tab struct {
		Teams  string `json:"teams"`
		Cookie string `json:"cookie"`
	}
	if err := json.Unmarshal([]byte(out), &tab); err != nil {
		return nil, fmt.Errorf("unexpected answer from the Slack tab in %s: %w", b.name, err)
	}
	teams, err := teamsFromObject(json.RawMessage(tab.Teams))
	if err != nil {
		return nil, fmt.Errorf("parsing the teams of the Slack tab in %s: %w", b.name, err)
	}
	if len(teams) == 0 {
		return nil, notFoundf("the Slack tab in %s lists no signed-in workspace", b.name)
	}

	extracted := &Extracted{Teams: teams, Source: b.name}
	// The "d" cookie is normally HttpOnly and therefore invisible to the
	// page; it is then read from the browser's cookie database.
	if strings.HasPrefix(tab.Cookie, "xoxd-") {
		extracted.CookieD = slackapi.DecodeCookieD(tab.Cookie)
		return extracted, nil
	}
	cookie, cookiesDB, err := h.browserCookieD(ctx, b)
	if err != nil {
		return nil, fmt.Errorf("%s has a Slack tab, but its session cookie could not be read: %w", b.name, err)
	}
	extracted.CookieD = cookie
	extracted.Paths.Cookies = cookiesDB
	return extracted, nil
}

// browserCookieD reads the xoxd cookie from the browser's profiles,
// "Default" first.
func (h *host) browserCookieD(ctx context.Context, b chromiumBrowser) (cookie, cookiesDB string, err error) {
	dataDir := filepath.Join(h.home, "Library", "Application Support", b.dataDir)
	profiles := []string{"Default"}
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "Profile ") {
			profiles = append(profiles, e.Name())
		}
	}
	slices.Sort(profiles[1:])

	keys := &safeStorage{h: h, macOS: b.keychain}
	var errs []error
	for _, profile := range profiles {
		for _, rel := range []string{filepath.Join("Network", "Cookies"), "Cookies"} {
			db := filepath.Join(dataDir, profile, rel)
			if !exists(db) {
				continue
			}
			cookie, err := h.chromiumCookieD(ctx, db, dataDir, keys)
			if err == nil {
				return cookie, db, nil
			}
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return "", "", fmt.Errorf("no cookie database under %s", dataDir)
	}
	// A profile without a Slack cookie is not worth reporting next to one
	// whose cookie could not be decrypted.
	if real := slices.DeleteFunc(slices.Clone(errs), func(e error) bool { return errors.Is(e, ErrNotFound) }); len(real) > 0 {
		errs = real
	}
	return "", "", errors.Join(errs...)
}
