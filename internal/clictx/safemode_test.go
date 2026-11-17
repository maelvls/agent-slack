package clictx

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The commands an agent could misuse to reach other people are refused,
// and the summary shown in the --safe-mode help names both what is
// covered and what is not.
func TestSafeModeBlocksOutreachAndCancellation(t *testing.T) {
	c := newContext(t)
	c.SafeModeFlag = true
	summary := SafeModeSummary()
	for command, action := range map[string]string{
		"channel invite":           "Slack Connect invitations",
		"channel new":              "create channels",
		"message scheduled cancel": "delete pending scheduled messages",
	} {
		err := c.CheckSafeMode(command)
		if err == nil || !strings.Contains(err.Error(), action) {
			t.Errorf("%q: err = %v, want a refusal mentioning %q", command, err, action)
		}
	}

	refused, notCovered, found := strings.Cut(summary, " are refused; ")
	if !found || !strings.HasSuffix(notCovered, " are NOT covered by safe mode") {
		t.Fatalf("summary = %q", summary)
	}
	for _, command := range SafeModeBlocked() {
		if !strings.Contains(refused, `"`+command+`"`) {
			t.Errorf("%q is not in the refused part of the summary: %s", command, summary)
		}
	}
	wantNotCovered := []string{"message react", "message draft *", "channel mark", "later *", "user dm-open"}
	if got := SafeModeNotCovered(); strings.Join(got, "|") != strings.Join(wantNotCovered, "|") {
		t.Errorf("SafeModeNotCovered() = %q, want %q", got, wantNotCovered)
	}
	for _, command := range wantNotCovered {
		if !strings.Contains(notCovered, `"`+command+`"`) {
			t.Errorf("%q is not in the not-covered part of the summary: %s", command, summary)
		}
	}
}

// docList matches a list of `backticked` commands introduced by a phrase.
func docList(phrase string) *regexp.Regexp {
	return regexp.MustCompile("(?i)" + phrase + ": ((?:`[^`]+`(?:, | and )?)+)")
}

var backticked = regexp.MustCompile("`([^`]+)`")

// The README and the skill list exactly the commands safe mode refuses
// and exactly those it does not cover.
func TestSafeModeDocsMatchTheCode(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../skills/slackcli/SKILL.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for phrase, want := range map[string][]string{
			"refused in safe mode":     SafeModeBlocked(),
			"not covered by safe mode": SafeModeNotCovered(),
		} {
			m := docList(phrase).FindStringSubmatch(string(raw))
			if m == nil {
				t.Errorf("%s: no list introduced by %q", path, phrase+":")
				continue
			}
			var got []string
			for _, item := range backticked.FindAllStringSubmatch(m[1], -1) {
				got = append(got, item[1])
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s: %s:\n  %q\nwant\n  %q", path, phrase, got, want)
			}
		}
	}
}
