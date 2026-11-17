package authextract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// localConfigKeys are the Local Storage keys under which the Slack web
// client keeps its signed-in teams.
var localConfigKeys = []string{"localConfig_v3", "localConfig_v2"}

// isSlackHost reports whether host belongs to Slack. Team URLs read from
// browser storage become the hosts that tokens and cookies are sent to, so
// anything else is refused.
func isSlackHost(host string) bool {
	host = strings.ToLower(host)
	for _, domain := range []string{"slack.com", "slack-gov.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// slackOrigin returns the origin of a Slack https URL, or "" if raw is not
// one.
func slackOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !isSlackHost(u.Hostname()) {
		return ""
	}
	return "https://" + strings.ToLower(u.Hostname())
}

// parseTeams extracts the signed-in teams from the JSON text of a Slack
// localConfig value, in the order Slack stores them. Entries without an
// xoxc token or with a non-Slack URL are dropped.
func parseTeams(config string) ([]Team, error) {
	var cfg struct {
		Teams json.RawMessage `json:"teams"`
	}
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		// Storage layers sometimes leave framing bytes around the JSON.
		start, end := strings.Index(config, "{"), strings.LastIndex(config, "}")
		if start < 0 || end <= start {
			return nil, err
		}
		if err2 := json.Unmarshal([]byte(config[start:end+1]), &cfg); err2 != nil {
			return nil, err
		}
	}
	return teamsFromObject(cfg.Teams)
}

// teamsFromObject decodes a JSON object {"T123": {url, name, token}, …},
// preserving key order (a Go map would not): the first team becomes the
// default workspace.
func teamsFromObject(raw json.RawMessage) ([]Team, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok == nil {
		return nil, nil // "teams": null
	} else if tok != json.Delim('{') {
		return nil, errors.New("teams is not a JSON object")
	}
	var teams []Team
	seen := map[string]bool{}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // team ID
			return nil, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		var fields map[string]any
		if json.Unmarshal(value, &fields) != nil {
			continue
		}
		rawURL, _ := fields["url"].(string)
		name, _ := fields["name"].(string)
		token, _ := fields["token"].(string)
		origin := slackOrigin(rawURL)
		if origin == "" || !strings.HasPrefix(token, "xoxc-") || seen[origin] {
			continue
		}
		seen[origin] = true
		teams = append(teams, Team{URL: rawURL, Name: name, Token: token})
	}
	return teams, nil
}
