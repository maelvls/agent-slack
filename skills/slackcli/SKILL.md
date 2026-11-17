---
name: slackcli
description: "Slack CLI for agents: read message URLs/threads/channel history/unreads/Later/canvases/workflows, search messages and files, download attachments, look up users, list/create/invite channels, open DMs, create/edit canvases from Markdown, hand a message to a human in the compose editor, manage Slack-native drafts, schedule sends, and explicit sends/edits/deletes/reactions/mark-read/uploads."
---

# slackcli

Use `slackcli` from `$PATH`. If it is missing, install it with:

```bash
curl -fsSL https://raw.githubusercontent.com/maelvls/slackcli/main/install.sh | sh
```

Fallback: `go install github.com/maelvls/slackcli/cmd/slackcli@latest`.

`slackcli --help` and each subcommand's `--help` are authoritative: they document every flag and the JSON each command prints. Read the relevant help before guessing. If a capability named here is absent from the installed help, report version skew instead of guessing. Do not run `slackcli update` without explicit authorization.

## Safety

- Read and search freely.
- Perform a write only when explicitly requested: `message send/edit/delete`, `message react`, `message scheduled cancel`, `message draft create/update/delete`, uploads (`--attach`), `channel new/invite/mark`, `user dm-open`, `later save/complete/archive/reopen/remove/remind`, `canvas create/edit`, `workflow run`. `workflow run` is not idempotent and can trigger downstream actions: read `workflow get` first.
- For a compose- or review-only request, return the proposed text without invoking Slack, or use `message draft create`: it posts nothing and leaves a draft in the user's Slack client.
- `message compose <target> [text]` hands the text to a human: it opens an editor in their browser and blocks, with no timeout, until they send or cancel. Use it only when the user asks for the editor. You cannot send from it yourself; without a browser it fails unless stderr is an interactive terminal. Exception: when the `CI` environment variable is set it skips the editor and sends `[text]` immediately, so in CI treat it as a send.
- Safe mode (`SLACKCLI_SAFE_MODE=1` or the global `--safe-mode`) is enforced by the tool: `message send` is redirected to the compose editor (and refuses `--attach`, `--blocks`, `--schedule`, `--schedule-in`, `--reply-broadcast`, and CI); `message edit`, `message delete`, `canvas create`, `canvas edit` and `workflow run` are refused. It does not cover reactions, `message scheduled cancel`, drafts, `channel`, `later` or `user dm-open`: the rule above still applies to those. Do not turn safe mode off to get a write through; tell the user.

## Workflow

1. Run `slackcli auth whoami` (local, redacted). Credentials are normally imported on the fly from Slack Desktop or a browser; if a command reports none, run `auth import-desktop` (or `import-chrome`, `import-brave`, `import-firefox`), then `auth test`.
2. Prefer a Slack message URL as the target: it carries workspace, channel and timestamp. Quote it.
3. Choose the narrowest read: `message get` for one message, `message list` for a thread or channel history, `search messages|files|all` for discovery, `unreads` for the inbox.
4. Bound the output with `--limit`, `--max-body-chars`, `--max-content-chars`, `--counts-only`, `--no-download`.
5. For a requested write, run only that mutation and check the JSON it prints.

## Non-obvious behaviour

- Channel history (`message list <channel>`) has top-level messages only; a message with `thread_ts` has a thread: list it to read replies. Thread listings reject history-only flags (`--limit`, `--oldest`, `--latest`, reaction filters). Reaction filters require `--oldest`.
- `search` without `--channel` uses Slack's search API (operators allowed; not available to bot tokens). With `--channel` (repeatable or comma-separated) the channels are scanned and matched locally: operators are rejected, thread replies are not seen, and it can be slow: bound it with `--after`. `--after`/`--before` are exclusive days.
- `later`, `unreads`, `message draft` and `channel list --via-counts` need browser credentials (xoxc + cookie). `canvas edit` and `canvas create --channel` need a standard token with `canvases:write`; browser credentials can only create standalone canvases.
- `message send` text is Slack mrkdwn; lists are converted to real Slack lists. Inside them write links as `<URL|label>`: CommonMark `[label](URL)` is not converted. `--blocks` replaces that conversion; with `--attach` the text is the files' plain comment. Several `--attach` flags make one message.
- Text starting with `- ` is accepted by `message send` and `message edit`; for `message compose` and `message draft create/update` put it after `--`. Write the `-1` emoji as `:-1:`.
- Scheduling: prefer `--schedule` with an ISO 8601 timestamp and explicit offset. `--schedule-in` day names, and `later remind --in` day names, use the local timezone of the machine running slackcli (9:00 by default): confirm it matches the user's intent or pass a Unix timestamp.
- `message draft update --attach` adds files; it never removes any. Drafts with a scheduled time or several destinations are refused.
- `canvas edit` applies one operation per call; the default `replace` without `--section-id` overwrites the whole canvas.
- `channel invite` exits non-zero on partial failure but still prints which invites went through: read the JSON before retrying.
- Commands are cut after 30 s (exit code 124; `SLACKCLI_COMMAND_TIMEOUT_MS`). A rate-limited call fails at once unless `SLACKCLI_RATE_LIMIT_MAX_WAIT_MS` is set.

## Conditional references

- Read [references/targets.md](references/targets.md) only when choosing between a message URL, channel or user target, or when several workspaces are configured.
- Read [references/output.md](references/output.md) only when handling returned message, search or write metadata, resolved users, or downloaded and failed attachments.
