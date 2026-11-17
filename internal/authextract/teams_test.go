package authextract

import "testing"

func TestParseTeams(t *testing.T) {
	teams, err := parseTeams(`{"other":1,"teams":{
		"T3":{"url":"https://c.slack.com/","name":"C","token":"xoxc-c"},
		"T1":{"url":"https://a.enterprise.slack.com/","token":"xoxc-a"},
		"T2":{"url":"https://b.slack.com/","name":"B","token":"xoxp-not-a-browser-token"},
		"T4":{"url":"https://evil.example/slack.com/","name":"Evil","token":"xoxc-evil"},
		"T5":{"url":"http://plain.slack.com/","token":"xoxc-plain"},
		"T6":{"url":"https://notslack.com/","token":"xoxc-lookalike"},
		"T7":"garbage",
		"T8":{"url":42,"token":"xoxc-x"},
		"T9":{"url":"https://c.slack.com/","name":"Duplicate","token":"xoxc-dup"},
		"T10":{"url":"https://g.slack-gov.com/","name":7,"token":"xoxc-gov"}
	}}`)
	if err != nil {
		t.Fatal(err)
	}
	// Order is Slack's own, not alphabetical.
	assertTeams(t, teams,
		Team{URL: "https://c.slack.com/", Name: "C", Token: "xoxc-c"},
		Team{URL: "https://a.enterprise.slack.com/", Token: "xoxc-a"},
		Team{URL: "https://g.slack-gov.com/", Token: "xoxc-gov"},
	)
}

func TestParseTeamsTolerance(t *testing.T) {
	// Framing bytes around the JSON.
	teams, err := parseTeams("\x02\x00" + testLocalConfig + "\x00")
	if err != nil {
		t.Fatal(err)
	}
	assertTeams(t, teams, wantTestTeams...)

	for _, in := range []string{`{}`, `{"teams":{}}`, `{"teams":null}`} {
		if teams, err := parseTeams(in); err != nil || len(teams) != 0 {
			t.Errorf("parseTeams(%s) = %v, %v; want no teams, no error", in, teams, err)
		}
	}
	for _, in := range []string{``, `not json`, `{"teams":[1]}`, `{"teams":`} {
		if _, err := parseTeams(in); err == nil {
			t.Errorf("parseTeams(%q) succeeded", in)
		}
	}
}

func TestSlackOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://acme.slack.com/":              "https://acme.slack.com",
		"https://ACME.Slack.com/api/x?y=1":     "https://acme.slack.com",
		"https://slack.com":                    "https://slack.com",
		"https://a.enterprise.slack.com/":      "https://a.enterprise.slack.com",
		"http://acme.slack.com/":               "",
		"https://acme.slack.com:8443/":         "",
		"https://user@acme.slack.com/":         "",
		"https://acme.slack.com.evil.example/": "",
		"https://evilslack.com/":               "",
		"acme.slack.com":                       "",
		"":                                     "",
	} {
		if got := slackOrigin(in); got != want {
			t.Errorf("slackOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeChromiumString(t *testing.T) {
	for _, s := range []string{"", "plain", "Acmé", "日本語 🎉"} {
		if got := decodeChromiumString(chromiumUTF16(s)); got != s {
			t.Errorf("UTF-16 %q decoded as %q", s, got)
		}
	}
	if got := decodeChromiumString(chromiumLatin1("Acmé")); got != "Acmé" {
		t.Errorf("Latin-1 decoded as %q", got)
	}
	if got := decodeChromiumString([]byte(`{"raw":1}`)); got != `{"raw":1}` {
		t.Errorf("unprefixed value decoded as %q", got)
	}
	if got := decodeChromiumString(nil); got != "" {
		t.Errorf("nil decoded as %q", got)
	}
}
