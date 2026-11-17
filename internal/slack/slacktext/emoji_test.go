package slacktext

import "testing"

func TestNormalizeReactionName(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"shortcode", ":rocket:", "rocket"},
		{"shortcode +1", ":+1:", "+1"},
		{"raw name", "rocket", "rocket"},
		{"raw +1", "+1", "+1"},
		{"raw name with underscores", "white_check_mark", "white_check_mark"},
		{"unicode emoji", "🚀", "rocket"},
		{"surrounding whitespace", "  :rocket: \n", "rocket"},
		{"custom emoji", ":party-parrot:", "party-parrot"},
		// Fixed in the port.
		{"uppercase is lowered, Slack names are lowercase", ":Rocket:", "rocket"},
		{"shortcode with skin tone", ":wave::skin-tone-3:", "wave::skin-tone-3"},
		{"raw name with skin tone", "+1::skin-tone-6", "+1::skin-tone-6"},
		{"unicode with skin tone", "👋🏽", "wave::skin-tone-4"},
		{"unicode thumbs up uses Slack's name", "👍", "+1"},
		{"unicode with variation selector", "❤️", "heart"},
		{"unicode without variation selector", "❤", "heart"},
		{"Slack name rather than GitHub's", "🤔", "thinking_face"},
		{"ZWJ sequence", "🤷‍♂️", "man-shrugging"},
		{"ZWJ sequence with skin tone", "🤷🏽‍♂️", "man-shrugging::skin-tone-4"},
		{"flag with a short alias", "🇫🇷", "fr"},
		{"flag without a short alias", "🇦🇫", "flag-af"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeReactionName(tt.in)
			if err != nil {
				t.Fatalf("NormalizeReactionName(%q): %v", tt.in, err)
			}
			assertEqual(t, got, tt.want)
		})
	}
}

func TestNormalizeReactionNameErrors(t *testing.T) {
	for _, in := range []string{"", "   ", "not an emoji", "::", "héllo wörld", "🚀🚀"} {
		if got, err := NormalizeReactionName(in); err == nil {
			t.Errorf("NormalizeReactionName(%q) = %q, want an error", in, got)
		}
	}
}

func TestEmojiShortcodesToUnicode(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no shortcode", "plain text", "plain text"},
		{"simple", "Ship it :rocket:", "Ship it 🚀"},
		{"adjacent", ":rocket::tada:", "🚀🎉"},
		{"+1", ":+1: and :-1:", "👍 and 👎"},
		{"unknown is kept", "our :partyparrot: rocks", "our :partyparrot: rocks"},
		{"custom then known", ":partyparrot::rocket:", ":partyparrot:🚀"},
		{"time is not an emoji", "at 12:30:45 sharp", "at 12:30:45 sharp"},
		{"lone colons", "a: b : c ::", "a: b : c ::"},
		// node-emoji's table (emojilib) lacks these Slack names.
		{"thumbsup", ":thumbsup:", "👍"},
		{"Slack-only names", ":thinking_face: :face_with_rolling_eyes: :man-shrugging:", "🤔 🙄 🤷‍♂️"},
		{"hyphenated flag", ":flag-fr: :flag-af: :fr:", "🇫🇷 🇦🇫 🇫🇷"},
		{"circled M", ":m:", "Ⓜ️"},
		{"skin tone", ":wave::skin-tone-3:", "👋🏼"},
		{"skin tone replaces the variation selector", ":point_up::skin-tone-6:", "☝🏿"},
		{"skin tone inside a ZWJ sequence", ":man-shrugging::skin-tone-4:", "🤷🏽‍♂️"},
		{"skin tone alone", ":skin-tone-2:", "🏻"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, EmojiShortcodesToUnicode(tt.in), tt.want)
		})
	}
}

func TestEmojiTableRoundTrips(t *testing.T) {
	table := loadEmojiTable()
	if len(table.byName) < 3000 || len(table.byChar) < 1800 {
		t.Fatalf("emoji table looks truncated: %d names, %d emoji", len(table.byName), len(table.byChar))
	}
	for char, name := range table.byChar {
		got, ok := emojiByName(name)
		if !ok {
			t.Errorf("preferred name %q of %q is not in the table", name, char)
			continue
		}
		if back, _ := emojiName(got); back != name {
			t.Errorf("emojiName(emojiByName(%q)) = %q", name, back)
		}
	}
}
