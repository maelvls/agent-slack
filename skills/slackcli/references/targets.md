# Target selection

Prefer a Slack message URL whenever one is available, and quote it (an unquoted `&` truncates it):

```text
https://<workspace>.slack.com/archives/<channel_id>/p<digits>[?thread_ts=...]
```

A message URL supplies the workspace, channel and message timestamp:

- `message get`, `edit`, `delete`, `react add/remove` act on that message. A `--ts` that contradicts the URL is an error.
- `message list` returns the whole thread the message belongs to.
- `message send` replies in that message's thread and refuses `--thread-ts`. `message compose` and `message draft create` reply in the thread too, unless `--thread-ts` overrides it.
- `channel mark` moves the read cursor to that message (`--ts` overrides the timestamp); `later save/complete/archive/reopen/remove/remind` act on that message and refuse `--ts`. Both refuse `--workspace` with a URL that names its workspace.

A Slack URL that points at a channel rather than a message is a channel target. Hosts other than `*.slack.com` / `*.slack-gov.com` are rejected.

Use a channel (`#name`, `name`, or a `C...`/`G...`/`D...` ID) when there is no URL:

- `message get`, `edit`, `delete`, `react`, `channel mark` and the `later` write commands require `--ts <seconds>.<micros>`.
- `message list` reads channel history, unless `--ts` or `--thread-ts` selects a thread.
- `message send` posts to the channel; add `--thread-ts` to reply.

A user ID (`U...` or `W...`, equivalent) means the direct message with that user. It is accepted by `message send`, `message compose`, `message draft create`, `message draft update --channel` and `message scheduled list/cancel --channel`. `message get/list/edit/delete/react` reject it: use `user dm-open <users...>` (one to 8 other users; IDs or emails are fastest) to get the `dm_channel_id` of a DM or group DM, then use that as a channel target.

Other argument kinds:

- `<canvas>`: a canvas URL (`https://<workspace>.slack.com/docs/T.../F...`), a channel-canvas URL (`https://<workspace>.slack.com/canvas/C...`) or a canvas ID (`F...`).
- `<trigger>` for `workflow preview/run`: a trigger ID (`Ft...`, from `workflow list`) or a shortcut URL (`https://slack.com/shortcuts/Ft.../...`). `workflow get` also takes a workflow ID (`Wf...`). `workflow run` requires `--channel`.
- Users (`user get`, `--user`, `--users`): a user ID, an email, `@handle`/`handle`; `user get`, `user dm-open` and `search --user` also take an exact display name. Handles and names scan the directory: prefer IDs or emails.

## Workspaces

Targets that are not workspace URLs carry no workspace. The workspace is then `--workspace <url-or-unique-substring>`, else `SLACK_WORKSPACE_URL`, else the default workspace (`auth whoami` shows it; `auth set-default` changes it). With several workspaces configured and no default, a channel name without `--workspace` is rejected as ambiguous. Pass `--workspace` for channel, user, canvas and workflow IDs that are not in the default workspace.
