package draftcmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/drafts"
	"github.com/maelvls/slackcli/internal/slack/scheduled"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slack/upload"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type listOptions struct {
	workspace string
	limit     string
	all       bool
}

type listOutput struct {
	OK     bool           `json:"ok"`
	Drafts []drafts.Draft `json:"drafts"`
	Count  int            `json:"count"`
}

// draftOutput is the result of create and update.
type draftOutput struct {
	OK    bool          `json:"ok"`
	Draft *drafts.Draft `json:"draft,omitempty"`
}

type deleteOutput struct {
	OK      bool   `json:"ok"`
	DraftID string `json:"draft_id"`
}

// draftsClient returns a client for workspace, refusing anything but
// browser auth before any other work is done with it.
func draftsClient(ctx context.Context, rt *clictx.Context, workspace string) (*slackapi.Client, error) {
	r, err := rt.Client(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if err := drafts.RequireBrowserAuth(r.Client); err != nil {
		return nil, err
	}
	return r.Client, nil
}

func list(ctx context.Context, rt *clictx.Context, opts listOptions) (listOutput, error) {
	limit, err := scheduled.ParseLimit(opts.limit)
	if err != nil {
		return listOutput{}, err
	}
	workspace := rt.EffectiveWorkspace(opts.workspace)
	return clictx.WithAutoRefresh(ctx, rt, func() (listOutput, error) {
		client, err := draftsClient(ctx, rt, workspace)
		if err != nil {
			return listOutput{}, err
		}
		found, err := drafts.List(ctx, client, drafts.ListOptions{Limit: limit, IncludeInactive: opts.all})
		if err != nil {
			return listOutput{}, err
		}
		nameChannels(ctx, client, found)
		return listOutput{OK: true, Drafts: found, Count: len(found)}, nil
	})
}

// maxNameLookups bounds the concurrent conversations.info calls of a list.
const maxNameLookups = 8

// nameChannels fills in the display name of every destination, best
// effort: a draft whose channel cannot be read is listed without a name.
func nameChannels(ctx context.Context, client *slackapi.Client, found []drafts.Draft) {
	var ids []string
	for _, d := range found {
		for _, dest := range d.Destinations {
			if !slices.Contains(ids, dest.ChannelID) {
				ids = append(ids, dest.ChannelID)
			}
		}
	}
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		names = map[string]string{}
		sem   = make(chan struct{}, maxNameLookups)
	)
	for _, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			// ResolveName answers the ID itself when it finds no name.
			if name := channels.ResolveName(ctx, client, id); name != id {
				mu.Lock()
				names[id] = name
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for i := range found {
		for j := range found[i].Destinations {
			dest := &found[i].Destinations[j]
			dest.ChannelName = names[dest.ChannelID]
		}
	}
}

type createOptions struct {
	workspace string
	threadTS  string
	broadcast bool
	attach    []string
}

func create(ctx context.Context, rt *clictx.Context, targetInput, text string, opts createOptions) (draftOutput, error) {
	target, err := slackurl.ParseTarget(targetInput)
	if err != nil {
		return draftOutput{}, err
	}
	if err := validateThreadTS(opts.threadTS); err != nil {
		return draftOutput{}, err
	}
	workspace, err := targetWorkspace(rt, target, opts.workspace)
	if err != nil {
		return draftOutput{}, err
	}
	// Reject impossible --broadcast combinations before any network
	// round-trip, so that the error names the real problem.
	if opts.broadcast {
		if err := checkBroadcastStatically(target, opts.threadTS); err != nil {
			return draftOutput{}, err
		}
	}
	paths := attachPaths(opts.attach)
	if strings.TrimSpace(text) == "" && len(paths) == 0 {
		return draftOutput{}, errors.New("draft text is empty: pass some text (or --attach a file)")
	}
	if err := upload.Check(paths); err != nil {
		return draftOutput{}, err
	}
	hintTruncatedURL(rt, target)

	// The uploaded files live outside the retried closure: a re-run after
	// an auth refresh must reuse them, not upload (and orphan) a second
	// copy.
	var files uploads
	out, err := clictx.WithAutoRefresh(ctx, rt, func() (draftOutput, error) {
		client, err := draftsClient(ctx, rt, workspace)
		if err != nil {
			return draftOutput{}, err
		}
		dest, err := resolveDestination(ctx, client, target, opts.threadTS)
		if err != nil {
			return draftOutput{}, err
		}
		// Backstop for URL targets, whose thread is only known here.
		if opts.broadcast {
			if err := checkBroadcast(dest, "--thread-ts or a message URL target"); err != nil {
				return draftOutput{}, err
			}
		}
		if err := files.ensure(ctx, client, paths); err != nil {
			return draftOutput{}, err
		}
		draft, err := drafts.Create(ctx, client, drafts.Content{
			ChannelID: dest.channelID,
			Text:      text,
			ThreadTS:  dest.threadTS,
			Broadcast: opts.broadcast,
			FileIDs:   files.ids,
		})
		if err != nil {
			return draftOutput{}, err
		}
		return draftOutput{OK: true, Draft: draft}, nil
	})
	if err != nil {
		files.discard(ctx, rt, workspace)
	}
	return out, err
}

type updateOptions struct {
	workspace string
	channel   string
	threadTS  string
	// broadcast is nil when neither --broadcast nor --no-broadcast was
	// passed: the draft's flag is then kept if its destination is.
	broadcast     *bool
	lastUpdatedTS string
	attach        []string
}

func update(ctx context.Context, rt *clictx.Context, draftID, text string, opts updateOptions) (draftOutput, error) {
	if err := drafts.ValidateID(draftID); err != nil {
		return draftOutput{}, err
	}
	if opts.lastUpdatedTS != "" {
		if err := drafts.ValidateTS(opts.lastUpdatedTS); err != nil {
			return draftOutput{}, fmt.Errorf("--last-updated-ts: %w", err)
		}
	}
	if err := validateThreadTS(opts.threadTS); err != nil {
		return draftOutput{}, err
	}
	var target *slackurl.Target
	workspace := rt.EffectiveWorkspace(opts.workspace)
	if opts.channel != "" {
		t, err := slackurl.ParseTarget(opts.channel)
		if err != nil {
			return draftOutput{}, fmt.Errorf("--channel: %w", err)
		}
		target = &t
		if workspace, err = targetWorkspace(rt, t, opts.workspace); err != nil {
			return draftOutput{}, err
		}
		if opts.broadcast != nil && *opts.broadcast {
			if err := checkBroadcastStatically(t, opts.threadTS); err != nil {
				return draftOutput{}, err
			}
		}
	}
	paths := attachPaths(opts.attach)
	if err := upload.Check(paths); err != nil {
		return draftOutput{}, err
	}
	if target != nil {
		hintTruncatedURL(rt, *target)
	}

	var files uploads // see create
	out, err := clictx.WithAutoRefresh(ctx, rt, func() (draftOutput, error) {
		client, err := draftsClient(ctx, rt, workspace)
		if err != nil {
			return draftOutput{}, err
		}
		// drafts.update rewrites the whole draft: start from the existing
		// one and override only what was asked.
		existing, err := drafts.Find(ctx, client, draftID)
		if err != nil {
			return draftOutput{}, err
		}
		// A draft is rewritten with one destination and no schedule, so
		// refuse those that would not survive the round-trip.
		if existing.DateScheduled != 0 {
			return draftOutput{}, fmt.Errorf("draft %s has a scheduled send time; updating it here would clear the schedule. Edit it in the Slack client, or delete and recreate it", draftID)
		}
		if target == nil && len(existing.Destinations) > 1 {
			return draftOutput{}, fmt.Errorf("draft %s targets multiple destinations; updating its text here would drop all but the first. Edit it in the Slack client, or re-address it with --channel", draftID)
		}
		lastUpdatedTS := existing.LastUpdatedTS
		if opts.lastUpdatedTS != "" {
			// Slack's own check is undocumented: detect the conflict
			// here, where the reason can be spelled out.
			if existing.LastUpdatedTS != "" && !drafts.SameTS(opts.lastUpdatedTS, existing.LastUpdatedTS) {
				return draftOutput{}, fmt.Errorf("draft %s changed since --last-updated-ts %s (it is now %s): read it again with \"draft list\" before overwriting it", draftID, opts.lastUpdatedTS, existing.LastUpdatedTS)
			}
			lastUpdatedTS = opts.lastUpdatedTS
		}
		if lastUpdatedTS == "" {
			return draftOutput{}, fmt.Errorf("draft %s has no last_updated_ts; pass --last-updated-ts", draftID)
		}

		var current drafts.Destination
		if len(existing.Destinations) > 0 {
			current = existing.Destinations[0]
		}
		var dest destination
		if target != nil {
			if dest, err = resolveDestination(ctx, client, *target, opts.threadTS); err != nil {
				return draftOutput{}, err
			}
		} else {
			dest = destination{channelID: current.ChannelID, threadTS: cmp.Or(opts.threadTS, current.ThreadTS)}
		}
		if dest.channelID == "" {
			return draftOutput{}, fmt.Errorf("draft %s has no destination; pass --channel", draftID)
		}
		// The existing broadcast flag is only inherited by an unchanged
		// destination (same channel, same thread): it must never follow
		// the reply into another thread's channel.
		broadcast := target == nil && dest.threadTS == current.ThreadTS && current.Broadcast
		if opts.broadcast != nil {
			broadcast = *opts.broadcast
		}
		if broadcast {
			if err := checkBroadcast(dest, "--thread-ts"); err != nil {
				return draftOutput{}, err
			}
		}
		if err := files.ensure(ctx, client, paths); err != nil {
			return draftOutput{}, err
		}
		draft, err := drafts.Update(ctx, client, draftID, lastUpdatedTS, drafts.Content{
			ChannelID: dest.channelID,
			Text:      text,
			ThreadTS:  dest.threadTS,
			Broadcast: broadcast,
			FileIDs:   mergeFileIDs(existing.FileIDs, files.ids),
		})
		if err != nil {
			return draftOutput{}, err
		}
		return draftOutput{OK: true, Draft: draft}, nil
	})
	if err != nil {
		// Only the new uploads are orphans: the files the draft already
		// had still belong to it.
		files.discard(ctx, rt, workspace)
	}
	return out, err
}

type deleteOptions struct {
	workspace     string
	lastUpdatedTS string
}

func remove(ctx context.Context, rt *clictx.Context, draftID string, opts deleteOptions) (deleteOutput, error) {
	if err := drafts.ValidateID(draftID); err != nil {
		return deleteOutput{}, err
	}
	if opts.lastUpdatedTS != "" {
		if err := drafts.ValidateTS(opts.lastUpdatedTS); err != nil {
			return deleteOutput{}, fmt.Errorf("--last-updated-ts: %w", err)
		}
	}
	workspace := rt.EffectiveWorkspace(opts.workspace)
	return clictx.WithAutoRefresh(ctx, rt, func() (deleteOutput, error) {
		client, err := draftsClient(ctx, rt, workspace)
		if err != nil {
			return deleteOutput{}, err
		}
		if err := drafts.Delete(ctx, client, draftID, opts.lastUpdatedTS); err != nil {
			return deleteOutput{}, err
		}
		return deleteOutput{OK: true, DraftID: draftID}, nil
	})
}

// uploads memoizes the files uploaded for one command across the retries
// of clictx.WithAutoRefresh.
type uploads struct {
	done bool
	ids  []string
}

// ensure uploads paths the first time it is called with any.
func (u *uploads) ensure(ctx context.Context, client *slackapi.Client, paths []string) error {
	if u.done || len(paths) == 0 {
		return nil
	}
	files, err := upload.ForDraft(ctx, client, paths)
	if err != nil {
		return err
	}
	u.done, u.ids = true, upload.IDs(files)
	return nil
}

// discard deletes the uploaded files after the draft call that was to
// reference them failed, so that they do not linger as private orphans.
// It is best effort and never masks the failure that led here.
func (u *uploads) discard(ctx context.Context, rt *clictx.Context, workspace string) {
	if len(u.ids) == 0 {
		return
	}
	r, err := rt.Client(ctx, workspace)
	if err == nil {
		err = upload.DeleteFiles(ctx, r.Client, u.ids)
	}
	if err != nil {
		rt.Hintf("Warning: could not delete the uploaded files that the failed draft left unused (%s): %v", strings.Join(u.ids, ", "), err)
	}
}

// attachPaths trims and de-duplicates --attach values, keeping their order.
func attachPaths(raw []string) []string {
	var out []string
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// mergeFileIDs appends the newly uploaded IDs to those the draft already
// has, without duplicates.
func mergeFileIDs(existing, added []string) []string {
	out := slices.Clone(existing)
	for _, id := range added {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
