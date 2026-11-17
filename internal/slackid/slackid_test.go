package slackid

import "testing"

func TestIsUserID(t *testing.T) {
	for in, want := range map[string]bool{
		"U12345678": true,
		"W12345678": true,
		"U1234":     false,
		"W1234":     false,
		"B12345678": false,
		"w12345678": false,
		"W1234-678": false,
	} {
		if got := IsUserID(in); got != want {
			t.Errorf("IsUserID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsChannelID(t *testing.T) {
	for in, want := range map[string]bool{
		"C12345678":  true,
		"D0ABCDEFGH": true,
		"G12345678":  true,
		"C1234":      false,
		"general":    false,
		"c12345678":  false,
		"U12345678":  false,
	} {
		if got := IsChannelID(in); got != want {
			t.Errorf("IsChannelID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseChannelInput(t *testing.T) {
	cases := map[string]ChannelInput{
		"#general":   {Value: "general"},
		" general ":  {Value: "general"},
		"C12345678":  {IsID: true, Value: "C12345678"},
		"#C12345678": {Value: "C12345678"}, // an explicit "#" always means a name
		"# spaced":   {Value: "spaced"},
		"":           {},
	}
	for in, want := range cases {
		if got := ParseChannelInput(in); got != want {
			t.Errorf("ParseChannelInput(%q) = %+v, want %+v", in, got, want)
		}
	}
}
