package canvas

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

var (
	// ErrBrowserChannel is returned when a channel canvas is requested
	// with browser credentials.
	ErrBrowserChannel = errors.New("adding a canvas as a channel tab requires a standard Slack token (xoxb-/xoxp- with the canvases:write scope, from SLACK_TOKEN or saved with \"auth add\"); imported browser credentials can create standalone canvases only: drop --channel or use a standard token")
	// ErrBrowserEdit is returned when an edit is attempted with browser
	// credentials.
	ErrBrowserEdit = errors.New("editing a canvas requires a standard Slack token (xoxb-/xoxp- with the canvases:write scope, from SLACK_TOKEN or saved with \"auth add\"); imported browser credentials cannot call canvases.edit")
	// ErrEmptyMarkdown is returned when there is nothing to put in a
	// canvas.
	ErrEmptyMarkdown = errors.New("canvas Markdown is empty")
)

// untitled is the title Slack's browser-side method needs when none is
// given.
const untitled = "Untitled"

// CreateInput describes a canvas to create.
type CreateInput struct {
	// Markdown is the content; it must not be blank.
	Markdown string
	// Title is optional.
	Title string
	// ChannelID, if set, adds the canvas as a tab of that channel
	// (standard tokens only).
	ChannelID string
}

// Created is a new canvas, in the shape the CLI prints.
type Created struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
}

// CreateResult is the result of Create.
type CreateResult struct {
	Canvas Created `json:"canvas"`
}

// Create creates a canvas from Markdown.
//
// A standard token uses the public canvases.create method. Browser
// credentials are not accepted by it, so they go through files.createCanvas,
// the method the Slack client itself uses: it only takes multipart bodies,
// needs a title, and cannot attach the canvas to a channel.
func Create(ctx context.Context, client *slackapi.Client, in CreateInput) (CreateResult, error) {
	if strings.TrimSpace(in.Markdown) == "" {
		return CreateResult{}, ErrEmptyMarkdown
	}
	title := strings.TrimSpace(in.Title)

	var id string
	if client.Auth().IsBrowser() {
		if in.ChannelID != "" {
			return CreateResult{}, ErrBrowserChannel
		}
		if title == "" {
			title = untitled
		}
		resp, err := client.APIMultipart(ctx, "files.createCanvas", map[string]any{
			"title":            title,
			"markdown":         in.Markdown,
			"loosenValidation": true,
		})
		if err != nil {
			return CreateResult{}, fmt.Errorf("creating the canvas: %w", err)
		}
		id = jsonx.Str(resp["file_id"])
	} else {
		params := map[string]any{
			"document_content": map[string]any{"type": "markdown", "markdown": in.Markdown},
		}
		if title != "" {
			params["title"] = title
		}
		if in.ChannelID != "" {
			params["channel_id"] = in.ChannelID
		}
		resp, err := client.API(ctx, "canvases.create", params)
		if err != nil {
			return CreateResult{}, fmt.Errorf("creating the canvas: %w%s", err, createHint(err, in.ChannelID))
		}
		id = jsonx.Str(resp["canvas_id"])
	}
	if id == "" {
		return CreateResult{}, errors.New("Slack reported success but returned no canvas ID: the canvas may have been created, check before retrying")
	}
	return CreateResult{Canvas: Created{ID: id, Title: title, ChannelID: in.ChannelID}}, nil
}

// createHint says what to do about the plan restrictions of
// canvases.create.
func createHint(err error, channelID string) string {
	switch {
	case slackapi.IsErrorCode(err, "free_teams_cannot_create_non_tabbed_canvases") && channelID == "":
		return " (free Slack plans only allow canvases attached to a channel: pass --channel)"
	case slackapi.IsErrorCode(err, "free_team_canvas_tab_already_exists"):
		return " (this channel already has a canvas and free Slack plans allow one per channel: edit that canvas instead)"
	}
	return ""
}
