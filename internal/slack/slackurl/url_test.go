package slackurl

import (
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/appinfo"
)

func TestParseMessageURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want MessageRef
	}{
		{
			name: "archives URL with p<digits>",
			in:   "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379",
			want: MessageRef{WorkspaceURL: "https://stablygroup.slack.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379"},
		},
		{
			name: "thread_ts from query",
			in:   "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379?thread_ts=1770160000.000001&cid=C060RS20UMV",
			want: MessageRef{WorkspaceURL: "https://stablygroup.slack.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379", ThreadTSHint: "1770160000.000001"},
		},
		{
			name: "thread_ts without cid is flagged as truncated",
			in:   "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379?thread_ts=1770160000.000001",
			want: MessageRef{WorkspaceURL: "https://stablygroup.slack.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379", ThreadTSHint: "1770160000.000001", PossiblyTruncated: true},
		},
		{
			name: "malformed thread_ts is ignored but still flags truncation",
			in:   "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379?thread_ts=abc",
			want: MessageRef{WorkspaceURL: "https://stablygroup.slack.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379", PossiblyTruncated: true},
		},
		{
			name: "enterprise grid host, upper-case host, trailing slash, surrounding space",
			in:   "  https://ACME.enterprise.slack.com/archives/C060RS20UMV/p1770165109628379/ ",
			want: MessageRef{WorkspaceURL: "https://acme.enterprise.slack.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379"},
		},
		{
			name: "GovSlack host",
			in:   "https://acme.slack-gov.com/archives/C060RS20UMV/p1770165109628379",
			want: MessageRef{WorkspaceURL: "https://acme.slack-gov.com", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379"},
		},
		{
			name: "DM channel",
			in:   "https://acme.slack.com/archives/D060RS20UMV/p1770165109628379",
			want: MessageRef{WorkspaceURL: "https://acme.slack.com", ChannelID: "D060RS20UMV", MessageTS: "1770165109.628379"},
		},
		{
			name: "app.slack.com thread view",
			in:   "https://app.slack.com/client/T012345AB/C060RS20UMV/thread/C060RS20UMV-1770165109.628379",
			want: MessageRef{TeamID: "T012345AB", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379", ThreadTSHint: "1770165109.628379"},
		},
		{
			name: "app.slack.com message",
			in:   "https://app.slack.com/client/T012345AB/C060RS20UMV/p1770165109628379",
			want: MessageRef{TeamID: "T012345AB", ChannelID: "C060RS20UMV", MessageTS: "1770165109.628379"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMessageURL(tt.in)
			if err != nil {
				t.Fatalf("ParseMessageURL: %v", err)
			}
			tt.want.Raw = tt.in
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseMessageURLErrors(t *testing.T) {
	tests := []struct {
		name, in, wantErr string
	}{
		{"not a URL", "general", "invalid URL"},
		{"other host", "https://example.com/archives/C060RS20UMV/p1770165109628379", "not a Slack workspace URL"},
		{"look-alike host", "https://evilslack.com/archives/C060RS20UMV/p1770165109628379", "not a Slack workspace URL"},
		{"host in userinfo", "https://acme.slack.com@evil.example/archives/C060RS20UMV/p1770165109628379", "not a Slack workspace URL"},
		{"userinfo on a Slack host", "https://user:pw@acme.slack.com/archives/C060RS20UMV/p1770165109628379", "unsupported Slack URL"},
		{"plain http", "http://acme.slack.com/archives/C060RS20UMV/p1770165109628379", "unsupported Slack URL"},
		{"other path", "https://acme.slack.com/files/U1/F1/x.png", "unsupported Slack URL path"},
		{"channel only", "https://acme.slack.com/archives/C060RS20UMV", "not at a message"},
		{"bad channel id", "https://acme.slack.com/archives/general/p1770165109628379", "unsupported Slack channel id"},
		{"bad message id", "https://acme.slack.com/archives/C060RS20UMV/1770165109628379", "unsupported Slack message id"},
		{"short message id", "https://acme.slack.com/archives/C060RS20UMV/p123456", "unsupported Slack message id"},
		{"app.slack.com thread for another channel", "https://app.slack.com/client/T012345AB/C060RS20UMV/thread/C999999999-1770165109.628379", "unsupported Slack thread id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMessageURL(tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("got error %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildMessageURL(t *testing.T) {
	tests := []struct {
		name                           string
		workspace, channel, ts, thread string
		want                           string
	}{
		{"root message", "https://stablygroup.slack.com/", "C060RS20UMV", "1770165109.628379", "",
			"https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379"},
		{"reply", "https://stablygroup.slack.com", "C060RS20UMV", "1770165110.000001", "1770165109.628379",
			"https://stablygroup.slack.com/archives/C060RS20UMV/p1770165110000001?thread_ts=1770165109.628379&cid=C060RS20UMV"},
		{"thread root given its own ts", "https://stablygroup.slack.com", "C060RS20UMV", "1770165109.628379", "1770165109.628379",
			"https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379"},
		{"ts that lost trailing zeros", "https://stablygroup.slack.com", "C060RS20UMV", "1770165109.6283", "",
			"https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildMessageURL(tt.workspace, tt.channel, tt.ts, tt.thread); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestBuildThenParseRoundTrip(t *testing.T) {
	link := BuildMessageURL("https://acme.slack.com", "C060RS20UMV", "1770165110.000001", "1770165109.628379")
	ref, err := ParseMessageURL(link)
	if err != nil {
		t.Fatal(err)
	}
	if ref.MessageTS != "1770165110.000001" || ref.ThreadTSHint != "1770165109.628379" || ref.PossiblyTruncated {
		t.Errorf("unexpected ref %+v", ref)
	}
}

func TestTruncatedURLHint(t *testing.T) {
	if got := TruncatedURLHint(MessageRef{}); got != "" {
		t.Errorf("no hint expected, got %q", got)
	}
	got := TruncatedURLHint(MessageRef{PossiblyTruncated: true})
	if !strings.Contains(got, "truncated by shell") || !strings.Contains(got, appinfo.Name+" message get") {
		t.Errorf("unexpected hint %q", got)
	}
}

func TestIsTSAndIsSlackURL(t *testing.T) {
	for s, want := range map[string]bool{"1770165109.628379": true, "1770165109": false, "1770165109.62": false, "abc.def": false} {
		if IsTS(s) != want {
			t.Errorf("IsTS(%q) = %v", s, !want)
		}
	}
	for s, want := range map[string]bool{
		"https://acme.slack.com/archives/C1": true, "https://acme.slack.com": true,
		"http://acme.slack.com": false, "https://example.com": false, "general": false,
	} {
		if IsSlackURL(s) != want {
			t.Errorf("IsSlackURL(%q) = %v", s, !want)
		}
	}
}
