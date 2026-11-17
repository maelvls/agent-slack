package slackurl

import (
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Target
	}{
		{"#channel", "#general", Target{Kind: TargetChannel, Channel: "#general"}},
		{"bare channel name", "general", Target{Kind: TargetChannel, Channel: "#general"}},
		{"channel ID", "C060RS20UMV", Target{Kind: TargetChannel, Channel: "C060RS20UMV"}},
		{"user ID", "U12345ABCDE", Target{Kind: TargetUser, UserID: "U12345ABCDE"}},
		{"W-prefixed user ID", "W12345ABCDE", Target{Kind: TargetUser, UserID: "W12345ABCDE"}},
		{"user ID with whitespace", "  U09GDJJKCCW  ", Target{Kind: TargetUser, UserID: "U09GDJJKCCW"}},
		{"short U-prefixed string is a channel name", "U1234", Target{Kind: TargetChannel, Channel: "#U1234"}},
		{"short W-prefixed string is a channel name", "W1234", Target{Kind: TargetChannel, Channel: "#W1234"}},
		{"channel URL", "https://acme.slack.com/archives/C060RS20UMV",
			Target{Kind: TargetChannel, Channel: "C060RS20UMV", WorkspaceURL: "https://acme.slack.com"}},
		{"app.slack.com channel URL", "https://app.slack.com/client/T012345AB/C060RS20UMV",
			Target{Kind: TargetChannel, Channel: "C060RS20UMV"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTarget(tt.in)
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseTargetMessageURL(t *testing.T) {
	got, err := ParseTarget("https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != TargetURL || got.Ref.ChannelID != "C060RS20UMV" || got.Ref.MessageTS != "1770165109.628379" ||
		got.WorkspaceURL != "https://stablygroup.slack.com" {
		t.Errorf("unexpected target %+v", got)
	}
}

func TestParseTargetErrors(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, err := ParseTarget(in); err == nil || !strings.Contains(err.Error(), "missing target") {
			t.Errorf("ParseTarget(%q): got %v, want a missing-target error", in, err)
		}
	}
	for _, in := range []string{"acme.slack.com/archives/C060RS20UMV/p1770165109628379", "general/random"} {
		if _, err := ParseTarget(in); err == nil || !strings.Contains(err.Error(), "unsupported target") {
			t.Errorf("ParseTarget(%q): got %v, want an unsupported-target error", in, err)
		}
	}
	// A URL that is not a Slack message link used to become the channel
	// name "#https://…".
	for _, in := range []string{
		"https://example.com/archives/C060RS20UMV/p1770165109628379",
		"https://acme.slack.com/files/U1/F1/x.png",
		"http://acme.slack.com/archives/C060RS20UMV/p1770165109628379",
	} {
		if _, err := ParseTarget(in); err == nil || !strings.Contains(err.Error(), "unsupported target URL") {
			t.Errorf("ParseTarget(%q): got %v, want an unsupported-URL error", in, err)
		}
	}
}
