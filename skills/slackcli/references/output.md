# JSON output and downloads

Results are JSON on stdout; hints and warnings go to stderr. `null`, blank strings, `[]` and `{}` are pruned, so test for a field's presence. Exceptions that print a line of text: `auth add`, `auth set-default`, `auth remove`, `auth import-chrome|import-brave|import-firefox`, `auth parse-curl`. Exit codes: 0 success, 1 error (message on stderr), 124 command timeout, 130 interrupted. Each command's `--help` states its exact output.

## Messages

A message is `{channel_id, ts, thread_ts?, author: {user_id | bot_id}, content, files?, reactions?, forwarded_threads?}`. `content` is Markdown, cut at `--max-body-chars`. `reactions` (`[{name, users, count?}]`) appear only with `--include-reactions`. A root message with replies carries its own `ts` as `thread_ts`.

- `message get`: `{message, thread?: {ts, length}}`. `thread` means the message belongs to a thread of `length` messages; read it with `message list` on the same target.
- `message list`: `{channel_id?, messages}`, oldest first. History mode sets `channel_id`; thread mode returns the root and all replies.
- Search results: `messages[]` have `channel_id, ts, author, content, files, permalink`; `files[]` have `title, mimetype, mode, path, permalink` (the file's permalink, not a message's). `more_messages` / `more_files` (`{total?, before?, hint}`) mean the list was cut: follow `hint`, usually by repeating the search with `--before <before>`.
- `unreads`: `channels[]` of `{channel_id, channel_name, channel_type, unread_count?, mention_count, messages?, messages_truncated?, messages_error?}` and `threads?: {has_unreads, mention_count}`. `unread_count` is a lower bound when `messages_truncated` is true.
- `later list`: `{counts, items[], next_cursor?}`; an item has `channel_id, channel_name, ts, state, date_saved, date_completed?, date_due?` and `message` or `message_error`.

User IDs are left as is. `--resolve-users` adds `referenced_users`, a map from user ID to `{id, name, real_name, display_name, email, title, tz, ...}`, served from a 24-hour per-workspace cache; `--refresh-users` fetches again.

Paged lists return `next_cursor` when there is more: pass it to `--cursor` with the same other flags.

## Writes

- `message send`: `{ok, channel_id, ts, thread_ts?, permalink}`. With `--schedule`/`--schedule-in`: `{ok, channel_id, scheduled_message_id, post_at, thread_ts?}` (keep `channel_id` and `scheduled_message_id` for `message scheduled cancel`). With `--attach`: `{ok, channel_id, thread_ts?, files: [{id, name, size}]}`, without `ts` or `permalink`.
- `message compose`: `{ok, sent, channel_id, ts, thread_ts?, permalink, text}` where `text` is what the human actually sent, or `{ok, cancelled, reason}` when nothing was sent. `ok: true` alone does not mean sent: check `sent`. Skipped in CI: `editor: "skipped"` and no `text`. A `message send` redirected by safe mode prints the same plus `safe_mode: true, redirected_from: "send"`.
- `message edit`, `delete`, `react add/remove`: `{ok: true}`.
- `message draft list`: `{ok, drafts, count}`; `create`/`update`: `{ok, draft}`; `delete`: `{ok, draft_id}`. A draft is `{id, text, destinations: [{channel_id, thread_ts?, broadcast?, channel_name?}], last_updated_ts, date_created, date_scheduled?, file_ids?}`.
- `channel invite`: `{channel_id, invited_user_ids, already_in_channel_user_ids, unresolved_users, failed: [{target, error}]}` (with `--external`: `invited_emails`, `already_invited_emails`, `failed`). Non-zero exit when anything is in `unresolved_users` or `failed`; the JSON is still printed.
- `later save|complete|archive|reopen|remove`: `{ok, action, channel_id, ts}`; `remind` adds `remind_at`, `remind_at_local`.
- `canvas create`: `{canvas: {id, title?, channel_id?}}`; `canvas edit`: `{ok, canvas: {id, operation}}`; `canvas get`: `{canvas: {id, title?, markdown, truncated?, total_chars?}}`. When `truncated` is true the Markdown was cut at `--max-chars`: pass `--max-chars -1` for all of it.
- `workflow run`: `{function_execution_id, trigger_execution_id, is_slow_workflow}`: Slack accepted the trigger, which says nothing about whether the workflow's steps succeeded.

## Downloaded files

`message get`, `message list` and `search` download attached files into the `downloads` directory of the OS cache directory (`~/Library/Caches/slackcli/downloads` on macOS, `$XDG_CACHE_HOME/slackcli/downloads` or `~/.cache/slackcli/downloads` on Linux). Canvases attached to messages are saved as Markdown (`.md`).

- A downloaded file entry has an absolute `path` plus `name` (or `title` for `search files`), `mimetype` and `mode`.
- A failed download keeps its entry, with `error` and a `path` to a `<file-id>.download-error.txt` file holding the reason; a warning is printed on stderr. This holds for `message get/list` and for every `search` command. Check for `error` before reading `path`.
- `search --no-download` lists files without `path`.
- For a download-then-reply task, use `search messages --content-type file`: its results carry the source message's `permalink`, which is a valid reply target.
