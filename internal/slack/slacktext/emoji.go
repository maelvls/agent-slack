// Package slacktext converts between Slack's text formats (mrkdwn, rich
// text blocks, attachments, canvas HTML) and Markdown, in both directions.
package slacktext

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// emojiData has one line per emoji: "<emoji>\t<name> <name> ...". The first
// name is the one Slack is most likely to know, and is the one used for
// reactions. It was generated from the kyokomi/emoji v2.2.13 code map (which
// merges the gemoji, Slack/iamcal and CLDR names) and emojilib 2.4.0 (what
// node-emoji used), both MIT-licensed, keeping only names made of
// [a-z0-9_+-] and dropping skin-tone variants, which are composed at runtime.
//
//go:embed emoji_data.txt
var emojiData string

const variationSelector16 = "️"

type emojiTable struct {
	byName map[string]string // shortcode → emoji
	byChar map[string]string // emoji without U+FE0F → preferred shortcode
}

var loadEmojiTable = sync.OnceValue(func() *emojiTable {
	t := &emojiTable{byName: map[string]string{}, byChar: map[string]string{}}
	for line := range strings.Lines(emojiData) {
		char, names, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		if !ok {
			continue
		}
		for i, name := range strings.Fields(names) {
			t.byName[name] = char
			if i == 0 {
				t.byChar[strings.ReplaceAll(char, variationSelector16, "")] = name
			}
		}
	}
	return t
})

const regionalIndicatorA = 0x1F1E6

// emojiByName returns the unicode emoji for a Slack shortcode (no colons).
func emojiByName(name string) (string, bool) {
	if char, ok := loadEmojiTable().byName[name]; ok {
		return char, true
	}
	// Slack names every country flag "flag-xx"; the table only has the ones
	// that have no shorter alias.
	if cc, ok := strings.CutPrefix(name, "flag-"); ok && len(cc) == 2 && isLowerASCII(cc[0]) && isLowerASCII(cc[1]) {
		return string([]rune{regionalIndicatorA + rune(cc[0]-'a'), regionalIndicatorA + rune(cc[1]-'a')}), true
	}
	return "", false
}

// emojiName returns the Slack shortcode of a unicode emoji that carries no
// skin-tone modifier.
func emojiName(char string) (string, bool) {
	char = strings.ReplaceAll(char, variationSelector16, "")
	if name, ok := loadEmojiTable().byChar[char]; ok {
		return name, true
	}
	if r := []rune(char); len(r) == 2 && isRegionalIndicator(r[0]) && isRegionalIndicator(r[1]) {
		return "flag-" + string([]rune{'a' + r[0] - regionalIndicatorA, 'a' + r[1] - regionalIndicatorA}), true
	}
	return "", false
}

func isLowerASCII(b byte) bool            { return b >= 'a' && b <= 'z' }
func isRegionalIndicator(r rune) bool     { return r >= regionalIndicatorA && r <= regionalIndicatorA+25 }
func isSkinToneModifier(r rune) bool      { return r >= 0x1F3FB && r <= 0x1F3FF }
func skinToneModifier(tone byte) string   { return string(rune(0x1F3FB + rune(tone-'2'))) }
func skinToneSuffix(modifier rune) string { return fmt.Sprintf("::skin-tone-%d", modifier-0x1F3FB+2) }

func isShortcodeByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '+' || b == '-'
}

// shortcodeAt parses a ":name:" shortcode starting at text[i] and returns the
// name and the index just after the closing colon.
func shortcodeAt(text string, i int) (name string, end int, ok bool) {
	if i >= len(text) || text[i] != ':' {
		return "", 0, false
	}
	j := i + 1
	for j < len(text) && isShortcodeByte(text[j]) {
		j++
	}
	if j == i+1 || j >= len(text) || text[j] != ':' {
		return "", 0, false
	}
	return text[i+1 : j], j + 1, true
}

// skinToneAt parses a ":skin-tone-N:" shortcode (N from 2 to 6) at text[i].
func skinToneAt(text string, i int) (tone byte, end int, ok bool) {
	name, end, ok := shortcodeAt(text, i)
	if !ok {
		return 0, 0, false
	}
	n, found := strings.CutPrefix(name, "skin-tone-")
	if !found || len(n) != 1 || n[0] < '2' || n[0] > '6' {
		return 0, 0, false
	}
	return n[0], end, true
}

// applySkinTone inserts a skin-tone modifier after the first code point of
// an emoji, which is where it belongs in ZWJ sequences too (🤷🏽‍♂️). The
// modifier replaces the variation selector of the base, if any (☝️ → ☝🏽).
func applySkinTone(char, modifier string) string {
	r := []rune(char)
	if len(r) == 0 {
		return char
	}
	rest := r[1:]
	if len(rest) > 0 && string(rest[0]) == variationSelector16 {
		rest = rest[1:]
	}
	return string(r[0]) + modifier + string(rest)
}

// EmojiShortcodesToUnicode replaces the Slack emoji shortcodes it knows
// (":rocket:", ":+1:", ":wave::skin-tone-3:", ":flag-fr:") with the unicode
// emoji, which is shorter for a model to read. Unknown shortcodes, such as
// a workspace's custom emoji, are left as they are.
func EmojiShortcodesToUnicode(text string) string {
	if !strings.Contains(text, ":") {
		return text
	}
	var b strings.Builder
	i := 0
	for i < len(text) {
		c := strings.IndexByte(text[i:], ':')
		if c < 0 {
			break
		}
		b.WriteString(text[i : i+c])
		i += c
		name, end, ok := shortcodeAt(text, i)
		if !ok {
			b.WriteByte(':')
			i++
			continue
		}
		char, known := emojiByName(name)
		if !known {
			// Consume the whole token, so that the closing colon of a custom
			// emoji is not taken for the opening colon of the next one.
			b.WriteString(text[i:end])
			i = end
			continue
		}
		if tone, toneEnd, ok := skinToneAt(text, end); ok {
			char = applySkinTone(char, skinToneModifier(tone))
			end = toneEnd
		}
		b.WriteString(char)
		i = end
	}
	b.WriteString(text[i:])
	return b.String()
}

var (
	colonReactionRE = regexp.MustCompile(`^:([^:\s]+)(::skin-tone-[2-6])?:$`)
	plainReactionRE = regexp.MustCompile(`^[A-Za-z0-9_+'-]+(::skin-tone-[2-6])?$`)
)

// NormalizeReactionName turns what a user may pass as an emoji — ":rocket:",
// "rocket", "+1", ":wave::skin-tone-3:" or a unicode emoji such as "🚀" or
// "👋🏽" — into the name the reactions.* API methods expect ("rocket",
// "wave::skin-tone-3").
func NormalizeReactionName(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("emoji is empty: pass a name such as rocket or :rocket:, or a unicode emoji")
	}
	if m := colonReactionRE.FindStringSubmatch(trimmed); m != nil {
		return strings.ToLower(m[1]) + m[2], nil
	}
	if plainReactionRE.MatchString(trimmed) {
		return strings.ToLower(trimmed), nil
	}

	var base []rune
	suffix := ""
	for _, r := range trimmed {
		if isSkinToneModifier(r) {
			if suffix == "" {
				suffix = skinToneSuffix(r)
			}
			continue
		}
		base = append(base, r)
	}
	if name, ok := emojiName(string(base)); ok {
		return name + suffix, nil
	}
	return "", fmt.Errorf("unsupported emoji %q: pass a Slack emoji name such as rocket or :rocket:, or a standard unicode emoji", input)
}
