---
name: agent-slack
description: "Slack CLI for agents: read URLs/threads/history/unreads/later/huddles/canvases/workflows, create/edit canvases from Markdown, search messages/files, download attachments, lookup users, list/create/invite channels, open DMs, compose messages, manage Slack-native drafts, schedule sends, and explicit sends/edits/deletes/reactions/mark-read/uploads."
---

# agent-slack

Use `agent-slack` from `$PATH`. If it is missing, install it with:

```bash
curl -fsSL https://raw.githubusercontent.com/stablyai/agent-slack/main/install.sh | sh
```

Fallback: `npm i -g agent-slack` (Node >= 22.5).

The common tasks below cover most needs; check `agent-slack <command> --help` for anything else instead of guessing a command or flag.
If a capability named here is absent from installed help, report version skew instead of guessing. Do not self-update the CLI without explicit authorization.

## Common tasks

```bash
# Read a whole thread: a message URL, or a channel plus any ts in the thread (root or reply)
agent-slack message list "https://<ws>.slack.com/archives/C123/p1790000000123456" --resolve-users
agent-slack message list C123 1790000000.123456

# Read one message
agent-slack message get "https://<ws>.slack.com/archives/C123/p1790000000123456"
agent-slack message get C123 1790000000.123456

# Channel or DM history (--oldest/--latest take a ts)
agent-slack message list C123 --limit 50

# Find a person by name, handle, email, or title, then fetch one
agent-slack user search "jane doe"
agent-slack user get "Jane Doe"     # also U123, @handle, or email; lists candidates if ambiguous

# Message someone: a U... target opens or reuses the DM
agent-slack message send U123 "text"
agent-slack user dm-open U123       # just the DM channel ID, e.g. to list its history

# Search (Slack query syntax works: from:@handle, in:#channel, before:/after:)
agent-slack search messages "query" --channel C123 --user @handle --after 2026-01-01 --limit 20
```

Pitfalls:

- stdout is always pure JSON for data commands; warnings and errors go to stderr. Pipe with `2>/dev/null | jq` or write stdout to a file. Never `2>&1 | jq`: an error then shows up as a jq parse error and the real message is lost.
- Result arrays (`messages`, `users`, `channels`, `files`) are present even when empty, so `{"messages": []}` means no results.
- A `ts` from search is often a reply. Pass it to `message list <channel> <ts>` to get the whole thread; do not assume it is the root.
- Bot messages have no `author.user_id`; use `.author.user_id // .author.bot_id` in jq.
- Don't wrap calls in `timeout` (absent on macOS by default). The CLI has its own watchdog, set with `AGENT_SLACK_COMMAND_TIMEOUT_MS`.
- Avoid paging `user list` to find someone; `user search` is one call.

## Safety

- Read and search freely.
- Perform write actions only when explicitly requested: sends, edits, deletes, reactions, invitations, channel or canvas creation/editing, mark-read operations, scheduling or canceling delivery, uploads, Later state/reminder changes, DM/group-DM creation, and `workflow run`. Workflow runs can execute downstream actions.
- For compose- or review-only requests, return proposed text without invoking Slack, or use `message draft create` to add a Slack-native draft the user can review and send (nothing is posted). `message compose` is send-capable; use it only when the user explicitly asks to open the interactive editor. In CI or another noninteractive environment, do not invoke it without separate authorization to send immediately: CI skips the editor and sends supplied text.
- With `AGENT_SLACK_SAFE_MODE=1` (or the global `--safe-mode` flag) set, safe mode is enforced at the tool level: `message send` is redirected to the draft editor, and other commands visible to people (edits, deletes, reactions, scheduled cancels, channel creation/invites, canvas edits, channel-tab canvases, `workflow run`) exit 1 with a "blocked" error. Don't retry those; hand the action to the user. Drafts, Later, and `channel mark` still work.

## Workflow

1. Run `agent-slack auth whoami`. If needed, import credentials with `auth import-desktop`, `auth import-brave`, `auth import-chrome`, or `auth import-firefox`, then run `auth test`.
2. Prefer a Slack message URL when one is available. It carries the workspace, channel, and timestamp needed by most message operations.
3. Choose the narrowest read operation: `message get` for one message, `message list` for a full thread or channel history, and `search messages` or `search files` for discovery.
4. Use output limits such as `--limit`, `--max-body-chars`, and `--max-content-chars` to avoid unnecessary context.
5. For a requested write, execute only the requested mutation and verify the resulting JSON metadata.

For scheduled writes, prefer `--schedule` with an ISO 8601 timestamp and explicit offset when timezone matters. Named `--schedule-in` phrases use the executing environment's local timezone; confirm that it matches the user's intent.

Named `later remind --in` values such as `tomorrow` or `monday` also use the executing environment's local timezone at 9:00. Confirm that timezone or pass an explicit Unix timestamp.

Use `--no-unfurl` with `message send` or `message compose` when the user wants Slack link and media previews suppressed. It cannot be combined with `message send --attach`.

Ordinary `message send` and `message edit` calls auto-convert lists. `message send --blocks` and `message edit --blocks` use supplied Block Kit blocks, while `message send --attach` sends its initial comment without automatic list conversion. Both Slack's `<URL|label>` syntax and CommonMark `[label](URL)` links become real link elements, in lists and elsewhere; CommonMark syntax inside a code span or fenced code block is left literal.

Links cannot be verified by reading a message back. `message get` renders a real link element, a literal `<URL|label>` string and literal CommonMark `[label](URL)` all identically as `[label](URL)`, so a broken link and a working one are indistinguishable in the output. `--resolve-users` discriminates real user mentions from literal `@Uxxx` text, but there is no equivalent for links: the only reliable check is a human looking at the rendered message. Do not report links as verified on the strength of a read-back.

Prefer editing over delete-and-resend when a message has replies: check `thread.length` from `message get` first, since deleting a thread root orphans the conversation under it.

Slack-native drafts (`message draft list|create|update|delete`) manage drafts that appear in the user's Slack client; `create` posts nothing. `create` and `update` accept repeatable `--attach <path>`; on `update` the files are added to the draft's existing attachments rather than replacing them. They use undocumented session endpoints and require browser-style auth (xoxc/xoxd).

`user org-chart [user]` returns the manager chain (nearest first), peers, and direct reports as user IDs; add `--resolve-users` for names.

`huddle list` reads huddle history (who, when, how long) newest-ended first; `--after`/`--before` take local `YYYY-MM-DD`. Huddle chat and notes live in the thread: `message list <channel_id> --thread-ts <thread_ts>`. It uses an undocumented endpoint and requires browser-style auth.

`canvas edit` uses Slack's public `canvases.edit` API and applies exactly one operation per call. The
default `replace` operation replaces the whole canvas; section-targeted inserts/replacements and
deletes require the section ID returned by Slack's Canvas tooling, while `rename` takes `--title`.
Content operations take exactly one `--file` or `--markdown` source. It requires a standard token
with `canvases:write`; imported browser credentials can create standalone canvases but cannot edit.

## Conditional references

- Read [references/targets.md](references/targets.md) only when choosing between a message URL, channel, or user target, or when resolving multiple workspaces.
- Read [references/output.md](references/output.md) only when handling returned message or canvas metadata, resolved users, or downloaded and failed attachments.
