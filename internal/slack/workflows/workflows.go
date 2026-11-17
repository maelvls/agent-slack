// Package workflows discovers and runs Slack workflows: the link triggers
// ("shortcuts") bookmarked or featured in a channel, their metadata, the
// workflow definition behind them, and tripping a trigger.
package workflows

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/enrich"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// Trigger identifies a workflow link trigger.
type Trigger struct {
	// ID is the trigger ID (Ft…).
	ID string
	// URL is the shortcut URL (https://slack.com/shortcuts/Ft…/…) when the
	// user gave one, else "". Run needs it; see ResolveShortcutURL.
	URL string
}

var triggerIDRe = regexp.MustCompile(`^Ft[A-Za-z0-9]+$`)

// ParseTrigger parses a <trigger-id> argument: a trigger ID (Ft…) or a
// workflow shortcut URL (https://slack.com/shortcuts/Ft…/<hash>).
func ParseTrigger(input string) (Trigger, error) {
	s := strings.TrimSpace(input)
	if triggerIDRe.MatchString(s) {
		return Trigger{ID: s}, nil
	}
	if id := triggerIDFromLink(s); id != "" {
		return Trigger{ID: id, URL: s}, nil
	}
	return Trigger{}, fmt.Errorf("invalid workflow trigger %q: expected a trigger ID (Ft…) or a shortcut URL (https://slack.com/shortcuts/Ft…/…); list a channel's workflows to find trigger IDs", input)
}

// triggerIDFromLink extracts the trigger ID from a shortcut URL on
// slack.com or a workspace subdomain ("" if link is not one).
func triggerIDFromLink(link string) string {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "slack.com" && !strings.HasSuffix(host, ".slack.com") {
		return ""
	}
	rest, ok := strings.CutPrefix(u.Path, "/shortcuts/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if !triggerIDRe.MatchString(id) {
		return ""
	}
	return id
}

// Workflow is a workflow trigger available in a channel.
type Workflow struct {
	Title     string `json:"title"`
	TriggerID string `json:"trigger_id"`
	Link      string `json:"link,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	Featured  bool   `json:"featured"`
}

// ChannelWorkflows is the result of List, in the shape the CLI prints.
type ChannelWorkflows struct {
	ChannelID string     `json:"channel_id"`
	Workflows []Workflow `json:"workflows"`
	// FeaturedErr is set when the featured-workflows lookup failed (the
	// method is not available on every workspace or token, and it may be
	// rate limited or time out like any other): Workflows then only holds
	// the bookmarked ones and every Featured flag is false. Callers should
	// surface it as a hint.
	FeaturedErr error `json:"-"`
}

// List returns the workflows bookmarked or featured in a channel:
// bookmarked ones first, then featured ones that are not bookmarked.
//
// The bookmarks are what the listing is made of: failing to list them is
// an error. The featured workflows are an enrichment (see package enrich):
// whatever goes wrong with them (unknown method, rate limit, timeout, a
// response that is not JSON) is reported in FeaturedErr next to the
// bookmarked workflows. Only dead credentials and a cancelled context
// fail the listing.
func List(ctx context.Context, client *slackapi.Client, channelID string) (ChannelWorkflows, error) {
	var (
		bookmarks             []map[string]any
		featured              []Workflow
		bookmarksErr, featErr error
		wg                    sync.WaitGroup
	)
	wg.Go(func() { bookmarks, bookmarksErr = listBookmarks(ctx, client, channelID) })
	wg.Go(func() { featured, featErr = listFeatured(ctx, client, channelID) })
	wg.Wait()
	if bookmarksErr != nil {
		return ChannelWorkflows{}, bookmarksErr
	}
	if enrich.MustAbort(ctx, featErr) {
		return ChannelWorkflows{}, featErr
	}

	isFeatured := map[string]bool{}
	for _, f := range featured {
		isFeatured[f.TriggerID] = true
	}
	out := ChannelWorkflows{ChannelID: channelID, FeaturedErr: featErr}
	seen := map[string]bool{}
	for _, b := range bookmarks {
		id := bookmarkTriggerID(b)
		if id == "" {
			continue // an ordinary link bookmark
		}
		seen[id] = true
		out.Workflows = append(out.Workflows, Workflow{
			Title:     jsonx.Str(b["title"]),
			TriggerID: id,
			Link:      jsonx.Str(b["link"]),
			AppID:     jsonx.Str(b["app_id"]),
			Featured:  isFeatured[id],
		})
	}
	for _, f := range featured {
		if !seen[f.TriggerID] {
			seen[f.TriggerID] = true
			out.Workflows = append(out.Workflows, f)
		}
	}
	return out, nil
}

func listBookmarks(ctx context.Context, client *slackapi.Client, channelID string) ([]map[string]any, error) {
	resp, err := client.API(ctx, "bookmarks.list", map[string]any{"channel_id": channelID})
	if err != nil {
		return nil, fmt.Errorf("listing bookmarks of channel %s: %w", channelID, err)
	}
	return jsonx.Maps(resp["bookmarks"]), nil
}

// bookmarkTriggerID is the trigger a bookmark points to: its shortcut_id,
// else the ID in its link.
func bookmarkTriggerID(bookmark map[string]any) string {
	if id := jsonx.Str(bookmark["shortcut_id"]); id != "" {
		return id
	}
	return triggerIDFromLink(jsonx.Str(bookmark["link"]))
}

func listFeatured(ctx context.Context, client *slackapi.Client, channelID string) ([]Workflow, error) {
	resp, err := client.API(ctx, "workflows.featured.list", map[string]any{"channel_ids": []string{channelID}})
	if err != nil {
		return nil, fmt.Errorf("listing featured workflows of channel %s: %w", channelID, err)
	}
	var out []Workflow
	for _, entry := range jsonx.Maps(resp["featured_workflows"]) {
		if jsonx.Str(entry["channel_id"]) != channelID {
			continue
		}
		for _, t := range jsonx.Maps(entry["triggers"]) {
			if id := jsonx.Str(t["id"]); id != "" {
				out = append(out, Workflow{Title: jsonx.Str(t["title"]), TriggerID: id, Featured: true})
			}
		}
		break
	}
	return out, nil
}

// Preview is a trigger's metadata, in the shape the CLI prints.
type Preview struct {
	TriggerID     string          `json:"trigger_id"`
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	ShortcutURL   string          `json:"shortcut_url,omitempty"`
	Workflow      PreviewWorkflow `json:"workflow"`
	Collaborators []string        `json:"collaborators"`
}

// PreviewWorkflow describes the workflow behind a trigger.
type PreviewWorkflow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	AppID       string `json:"app_id"`
	AppName     string `json:"app_name"`
}

// PreviewTrigger returns a trigger's metadata without running it
// (workflows.triggers.preview). triggerID is a Trigger.ID.
func PreviewTrigger(ctx context.Context, client *slackapi.Client, triggerID string) (Preview, error) {
	resp, err := client.API(ctx, "workflows.triggers.preview", map[string]any{"trigger_ids": triggerID})
	if err != nil {
		return Preview{}, fmt.Errorf("previewing trigger %s: %w", triggerID, err)
	}
	triggers := jsonx.Maps(resp["triggers"])
	if len(triggers) == 0 {
		if len(jsonx.Slice(resp["rejected_triggers"])) > 0 {
			return Preview{}, fmt.Errorf("trigger %s was rejected by Slack: it does not exist in this workspace or you do not have access to it", triggerID)
		}
		return Preview{}, fmt.Errorf("Slack returned no preview data for trigger %s", triggerID)
	}
	t := triggers[0]
	wf := jsonx.Map(t["workflow"])
	p := Preview{
		TriggerID:   jsonx.Str(t["id"]),
		Type:        jsonx.Str(t["type"]),
		Name:        jsonx.Str(t["name"]),
		Description: jsonx.Str(t["description"]),
		ShortcutURL: jsonx.Str(t["shortcut_url"]),
		Workflow: PreviewWorkflow{
			ID:          jsonx.Str(wf["workflow_id"]),
			Title:       jsonx.Str(wf["title"]),
			Description: jsonx.Str(wf["description"]),
			AppID:       jsonx.Str(wf["app_id"]),
			AppName:     jsonx.GetStr(wf, "app", "name"),
		},
	}
	if p.TriggerID == "" {
		p.TriggerID = triggerID
	}
	if p.Workflow.AppID == "" {
		p.Workflow.AppID = jsonx.GetStr(wf, "app", "id")
	}
	for _, c := range jsonx.GetSlice(t, "workflow_details", "collaborators") {
		if s := jsonx.Str(c); s != "" {
			p.Collaborators = append(p.Collaborators, s)
		}
	}
	return p, nil
}

// ResolveShortcutURL finds the shortcut URL that Run needs for a trigger
// given by ID only: from the channel's bookmarks, else (for workflows that
// are featured but not bookmarked) from the trigger's preview.
func ResolveShortcutURL(ctx context.Context, client *slackapi.Client, channelID, triggerID string) (string, error) {
	bookmarks, err := listBookmarks(ctx, client, channelID)
	if err != nil {
		return "", err
	}
	for _, b := range bookmarks {
		if link := jsonx.Str(b["link"]); link != "" && bookmarkTriggerID(b) == triggerID {
			return link, nil
		}
	}
	preview, err := PreviewTrigger(ctx, client, triggerID)
	if err != nil {
		return "", fmt.Errorf("trigger %s is not bookmarked in channel %s and its shortcut URL could not be looked up: %w", triggerID, channelID, err)
	}
	if preview.ShortcutURL == "" {
		return "", fmt.Errorf("could not find a shortcut URL for trigger %s: it is not bookmarked in channel %s and Slack does not report one; pass the full shortcut URL (https://slack.com/shortcuts/%s/…) instead", triggerID, channelID, triggerID)
	}
	return preview.ShortcutURL, nil
}

// RunInput is what Run needs to trip a trigger.
type RunInput struct {
	// ShortcutURL is Trigger.URL, or the result of ResolveShortcutURL.
	ShortcutURL string
	// ChannelID is the conversation the workflow is run from.
	ChannelID string
	// TriggerID is Trigger.ID.
	TriggerID string
}

// RunResult is the outcome of Run, in the shape the CLI prints.
type RunResult struct {
	FunctionExecutionID string `json:"function_execution_id"`
	TriggerExecutionID  string `json:"trigger_execution_id"`
	IsSlowWorkflow      bool   `json:"is_slow_workflow"`
}

// Run trips a workflow trigger as if the user had clicked its bookmark in
// the channel (workflows.triggers.trip). This starts the workflow: it is
// not idempotent.
func Run(ctx context.Context, client *slackapi.Client, in RunInput) (RunResult, error) {
	if in.ShortcutURL == "" || in.ChannelID == "" || in.TriggerID == "" {
		return RunResult{}, errors.New("running a workflow needs a shortcut URL, a channel ID and a trigger ID")
	}
	resp, err := client.API(ctx, "workflows.triggers.trip", map[string]any{
		"url": in.ShortcutURL,
		// The random part keeps two runs in the same millisecond distinct.
		"client_token": fmt.Sprintf("cli-%d-%s", time.Now().UnixMilli(), rand.Text()[:8]),
		"context": map[string]any{
			"location":   "bookmark",
			"channel_id": in.ChannelID,
			"trigger_id": in.TriggerID,
		},
		"run_precheck": true,
	})
	if err != nil {
		return RunResult{}, fmt.Errorf("running trigger %s: %w", in.TriggerID, err)
	}
	return RunResult{
		FunctionExecutionID: jsonx.Str(resp["function_execution_id"]),
		TriggerExecutionID:  jsonx.Str(resp["trigger_execution_id"]),
		IsSlowWorkflow:      jsonx.Bool(resp["is_slow_workflow"]),
	}, nil
}
