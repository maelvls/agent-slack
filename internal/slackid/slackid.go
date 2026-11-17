// Package slackid recognises Slack identifiers.
package slackid

import (
	"regexp"
	"strings"
)

var (
	channelIDRe = regexp.MustCompile(`^[CDG][A-Z0-9]{8,}$`)
	userIDRe    = regexp.MustCompile(`^[UW][A-Z0-9]{8,}$`)
)

// IsChannelID reports whether s looks like a conversation ID (C…, D…, G…).
func IsChannelID(s string) bool { return channelIDRe.MatchString(s) }

// IsUserID reports whether s looks like a user ID (U… or W…).
func IsUserID(s string) bool { return userIDRe.MatchString(s) }

// ChannelInput is a channel reference given by the user.
type ChannelInput struct {
	// IsID is true when Value is a conversation ID, false when it is a name.
	IsID bool
	// Value is the ID, or the name without its leading "#".
	Value string
}

// ParseChannelInput classifies "#general", "general" or "C0123ABCD".
func ParseChannelInput(input string) ChannelInput {
	s := strings.TrimSpace(input)
	if strings.HasPrefix(s, "#") {
		return ChannelInput{Value: strings.TrimSpace(s[1:])}
	}
	if IsChannelID(s) {
		return ChannelInput{IsID: true, Value: s}
	}
	return ChannelInput{Value: s}
}
