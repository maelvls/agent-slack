# slackcli

Slack automation CLI for AI agents: a single static Go binary that reads, searches and writes Slack as you.

- **Token-efficient**: results are compact JSON with `null`, blank and empty fields pruned, so an LLM can consume them cheaply.
- **Zero-config auth**: credentials are read from Slack Desktop, with Chrome, Brave and Firefox as fallbacks. Standard Slack tokens work too.
- **Human-in-the-loop**: `message compose` opens an editor in your browser where a person reviews and sends; safe mode makes that review mandatory.

slackcli is a Go rewrite of [stablyai/agent-slack](https://github.com/stablyai/agent-slack) (MIT). See [Migrating from agent-slack](#migrating-from-agent-slack).

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/maelvls/slackcli/main/install.sh | sh
```

The script downloads the release archive for your machine, verifies its SHA-256 against the release's `checksums.txt` (this cannot be skipped) and installs the binary into `~/.local/bin`. Set `PREFIX` to install into `$PREFIX/bin`, `SLACKCLI_INSTALL_DIR` to choose the directory, and `SLACKCLI_VERSION=v1.2.3` to pin a release.

Alternatives:

```bash
go install github.com/maelvls/slackcli/cmd/slackcli@latest
```

or download `slackcli_<darwin|linux|windows>_<amd64|arm64>.tar.gz` (`.zip` on Windows) and `checksums.txt` from the [GitHub releases](https://github.com/maelvls/slackcli/releases).

## Quick start

```bash
slackcli auth import-desktop     # or just run a command: credentials are imported on the fly
slackcli auth test

slackcli message get "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379"
slackcli message list "#general" --limit 20
slackcli search messages "deploy failed" --after 2026-01-01
slackcli message send "#general" "Hello from slackcli"
```

`slackcli --help` and `slackcli <command> --help` are the reference for every command, flag and output; this README only gives the map.

## Agent skill

The repository ships an agent skill in `skills/slackcli/` (Claude Code, Codex and other agents that read `SKILL.md`). From a checkout, install it into `~/.claude/skills` and `~/.agents/skills` with:

```bash
bash ./scripts/install-skill.sh
```

## Authentication

Credentials are looked up in this order:

1. The `SLACK_TOKEN` environment variable (with `SLACK_COOKIE_D` when it is an `xoxc-` browser token).
2. The workspaces saved by the `auth` commands.
3. Slack Desktop (macOS, Linux, Windows), then Chrome and Brave (macOS), then Firefox (macOS, Linux), imported on the fly and saved.

When saved browser credentials have expired, they are re-imported from Slack Desktop once and the command is retried.

Manual imports and checks:

```bash
slackcli auth whoami            # configured workspaces, default, redacted tokens (local only)
slackcli auth test              # calls Slack's auth.test
slackcli auth import-desktop
slackcli auth import-chrome     # also import-brave; needs "Allow JavaScript from Apple Events"
slackcli auth import-firefox
pbpaste | slackcli auth parse-curl   # a Slack API request copied as cURL, read from stdin
slackcli auth set-default acme
slackcli auth remove acme
```

Save a token without putting it on the command line:

```bash
SLACK_TOKEN=xoxb-... slackcli auth add --workspace-url https://acme.slack.com --from-env
```

Or use environment variables only:

```bash
export SLACK_TOKEN="xoxc-..."                        # browser token
export SLACK_COOKIE_D="xoxd-..."                     # the browser's "d" cookie
export SLACK_WORKSPACE_URL="https://acme.slack.com"  # required with a browser token unless the target is a Slack URL
```

### Browser tokens and standard tokens

| | Browser session (`xoxc-` token + `d` cookie) | Standard token (`xoxp-` user, `xoxb-` bot) |
| --- | --- | --- |
| Source | Slack Desktop or a browser, imported automatically | Created in a Slack app; `SLACK_TOKEN` or `auth add` |
| Acts as | You, with everything your Slack client can do | The app, limited to its scopes |
| Required for | `later`, `unreads`, `message draft`, `channel list --via-counts` | `canvas edit`, `canvas create --channel` (scope `canvases:write`) |
| Cannot do | `canvas edit`, `canvas create --channel` | The commands in the cell on the left; a bot token (`xoxb-`) also cannot use Slack's search API (`search --channel` still works) |

Commands that need a browser session use Slack's internal, undocumented client endpoints: they act only as you, but their behaviour may change without notice.

## Command map

```text
slackcli
├── auth
│   ├── whoami | test | add | set-default <workspace> | remove <workspace>
│   ├── import-desktop | import-chrome | import-brave | import-firefox
│   └── parse-curl
├── message [<target>]              # short for "message get <target>"
│   ├── get     <target>            # one message (+ thread summary)
│   ├── list    <target>            # channel history, or a whole thread
│   ├── send    <target> [text]     # send, reply or schedule; --attach, --blocks
│   ├── edit    <target> <text>
│   ├── delete  <target>
│   ├── react   add|remove <target> <emoji>
│   ├── scheduled [list] | cancel <id>
│   ├── compose <target> [text]     # browser editor; a human sends
│   └── draft   list | create <target> <text> | update <draft-id> <text> | delete <draft-id>
├── search  all|messages|files <query>
├── channel list | new | invite | mark <target>
├── user    list | get <user> | dm-open <users...>
├── later   [list] | save|complete|archive|reopen|remove|remind <target>
├── unreads
├── canvas  create | get <canvas> | edit <canvas>
├── workflow list <channel> | preview <trigger> | get <id> | run <trigger>
└── update  [--check]
```

Global flag: `--safe-mode`. Most commands take `--workspace`.

## Targets

Most commands take a `<target>`:

- **Message URL** `https://<workspace>.slack.com/archives/<channel-id>/p<digits>[?thread_ts=...]`: names the workspace, the channel and the message. Prefer it. Quote it: an unquoted `&` is cut by the shell.
- **Channel**: `#general`, `general`, or an ID (`C...`, `G...`, `D...`). Commands that act on one message then need `--ts`.
- **User ID** (`U...`/`W...`): the direct message with that user. Accepted by `message send`, `message compose`, `message draft create`, and the `--channel` flag of `message draft update` and `message scheduled`.

```bash
slackcli message get "#general" --ts 1770165109.628379
slackcli message list "#general" --thread-ts 1770165109.000001
```

With several workspaces configured, a channel name is ambiguous: pass `--workspace <url-or-unique-substring>`, set `SLACK_WORKSPACE_URL`, or run `slackcli auth set-default`. A Slack URL target names its own workspace.

## Examples

### Read

```bash
# One message, with a thread summary if it has a thread
slackcli message get "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379"

# The whole thread that message belongs to
slackcli message list "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379"

# Recent channel history (top-level messages only, at most 1000 per call)
slackcli message list "#general" --limit 50 --resolve-users

# Channel history filtered by reaction (needs --oldest to bound the scan)
slackcli message list "#general" --with-reaction eyes --oldest 1770165109.000000

# Inbox: every conversation with unread messages (nothing is marked as read)
slackcli unreads --max-messages 5
```

Files attached to messages are downloaded and listed with their absolute local `path`.

### Search

```bash
# Slack's search API, with its operators
slackcli search messages 'in:#alerts "smoke tests failed"' --after 2026-01-01 --before 2026-02-01

# Scan specific channels and match locally (works with any token)
slackcli search all "rollback" --channel "#alerts,#deploys" --after 2026-01-01

# Files, without downloading them
slackcli search files "roadmap" --content-type image --no-download
```

`--after` and `--before` are exclusive days. When more results match than `--limit`, the output has `more_messages` / `more_files` with a hint on how to continue.

### Write

```bash
slackcli message send "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379" "I can take this."
slackcli message send "#general" "Report attached" --attach ./report.md --attach ./chart.png   # one message, both files
slackcli message send "#announcements" "Deploy starts at 6pm." --schedule "2026-06-15T18:00:00-07:00"
slackcli message send U0123ABCDEF "Heads up before standup" --schedule-in "monday 9am"
slackcli message send "#general" "fallback text" --blocks ./blocks.json
slackcli message edit "#general" "Updated text" --ts 1770165109.628379
slackcli message react add "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379" eyes
slackcli message scheduled list
slackcli message scheduled cancel Q1234ABCD --channel C0123ABCDEF
```

Text is Slack mrkdwn; bullet and numbered lists are sent as real Slack lists, and text may start with `- `.

### Compose and drafts

```bash
# Open the editor in the browser, pre-filled; a human edits, then sends or cancels
slackcli message compose "#general" "Here's my update"

# Create a Slack-native draft: nothing is posted, it appears in the user's Slack client
slackcli message draft create "#general" "Here's my update" --attach ./q3.png
slackcli message draft list
```

`message compose` waits for the human (no timeout) and prints what was sent (`ts`, `permalink`, `text`) or `{ok, cancelled, reason}`. The editor only works from a real browser session: when no browser can be opened, the command prints a one-time link only if stderr is an interactive terminal, and fails otherwise. When the `CI` environment variable is set, the editor is skipped and the supplied text is sent immediately (except in safe mode, where the command fails).

### Channels and users

```bash
slackcli channel list --limit 50
slackcli channel new --name incident-war-room --private
slackcli channel invite --channel incident-war-room --users "U0123ABCDEF,@alice,bob@example.com"
slackcli channel mark "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379"
slackcli user get alice@example.com
slackcli user dm-open @alice @bob
```

`channel invite` tries every target, prints what happened to each (`invited_user_ids`, `already_in_channel_user_ids`, `unresolved_users`, `failed`) and exits non-zero when any target was unresolved or refused.

### Later, canvases, workflows

```bash
slackcli later list --state all
slackcli later remind "https://acme.slack.com/archives/C0123ABCDEF/p1770165109628379" --in "tomorrow at 9:30am"

slackcli canvas get "https://acme.slack.com/docs/T0123ABCD/F0123ABCDEF"
generate-report | slackcli canvas create --file - --title "Weekly report"
slackcli canvas edit F0123ABCDEF --operation insert_at_end --markdown "- [ ] One more thing"

slackcli workflow list "#helpdesk"
slackcli workflow get Ft0123ABCDEF
slackcli workflow run Ft0123ABCDEF --channel "#helpdesk"   # starts the workflow: not idempotent
```

## Safe mode

Skill instructions such as "never send without asking" are guidance an agent can ignore. Safe mode enforces human review in the tool itself:

```bash
export SLACKCLI_SAFE_MODE=1                       # 1, true, yes or on
slackcli --safe-mode message send "#general" "hello"
```

While safe mode is on:

| Command | Behaviour |
| --- | --- |
| `message send` | Nothing is posted: the compose editor opens with the text for a human to send. The output adds `"safe_mode": true` and `"redirected_from": "send"`. `--attach`, `--blocks`, `--schedule`, `--schedule-in` and `--reply-broadcast` are refused, and so is running under `CI`. |
| `message compose` | Unchanged, except under `CI`, where it fails instead of sending. |
| `message edit`, `message delete` | Refused. |
| `canvas create`, `canvas edit` | Refused. |
| `workflow run` | Refused. |

Safe mode does **not** cover `message react add/remove`, `message scheduled cancel`, `message draft`, `channel new/invite/mark`, `later` or `user dm-open`: these still take effect immediately.

## Output conventions

- Results are JSON on stdout, indented with two spaces, with `null`, blank strings, empty arrays and empty objects removed. Hints and warnings go to stderr.
- Some `auth` commands (`add`, `set-default`, `remove`, `import-chrome`, `import-brave`, `import-firefox`, `parse-curl`) print a line of text instead.
- Messages are compact: `channel_id`, `ts`, `thread_ts`, `author` (`user_id` or `bot_id`), `content` (Markdown), `files`, `reactions`. User IDs are kept as is; `--resolve-users` adds their profiles under `referenced_users`.
- Long content is cut at `--max-body-chars` / `--max-content-chars`; lists are paged with `--limit` and `--cursor` (`next_cursor` in the output).
- Downloaded files are listed with an absolute `path`; a failed download keeps its entry with an `error`.

## Environment variables

| Variable | Effect |
| --- | --- |
| `SLACK_TOKEN` | Token used instead of the saved workspaces (`xoxb-`, `xoxp-`, or `xoxc-`). |
| `SLACK_COOKIE_D` | The browser `d` cookie (`xoxd-...`), required when `SLACK_TOKEN` is an `xoxc-` token. `SLACK_COOKIE` is read as a fallback. |
| `SLACK_WORKSPACE_URL` | Default for `--workspace`. |
| `SLACKCLI_SAFE_MODE` | `1`, `true`, `yes` or `on` turns safe mode on. |
| `SLACKCLI_COMMAND_TIMEOUT_MS` | Time limit of a whole command (default 30000). Does not apply to `message compose` or `update`. |
| `SLACKCLI_API_TIMEOUT_MS` | Time limit of one Slack API request (default 20000). |
| `SLACKCLI_RATE_LIMIT_MAX_WAIT_MS` | How long to wait and retry when Slack rate-limits a request (default 0: fail at once). |
| `SLACKCLI_KEYCHAIN_TIMEOUT_MS` | Time limit of a macOS keychain call (default 3000). |
| `SLACKCLI_NO_UPDATE_CHECK` | `1` disables the background update notice. |
| `CI` | When set (other than `0`/`false`): no update notice, and `message compose` skips the editor. |
| `XDG_CONFIG_HOME` | Moves the configuration directory (absolute path). |
| `SLACKCLI_INSTALL_DIR`, `SLACKCLI_VERSION`, `PREFIX` | Read by `install.sh` only. |

For the legacy prefix of the `SLACKCLI_*` variables, see [Migrating from agent-slack](#migrating-from-agent-slack).

## Files and locations

| What | Where |
| --- | --- |
| Credentials | `$XDG_CONFIG_HOME/slackcli/credentials.json`, else `~/.config/slackcli/credentials.json` (mode 0600), on every OS |
| Secrets on macOS | Login keychain, service `slackcli`; the credentials file then holds placeholders. On Linux and Windows the secrets stay in the credentials file. |
| Cache (user cache, update-check state) | The OS user cache directory + `/slackcli`: `~/Library/Caches/slackcli` on macOS, `$XDG_CACHE_HOME/slackcli` or `~/.cache/slackcli` on Linux, `%LocalAppData%\slackcli` on Windows |
| Downloads (attachments, canvases) | `downloads/` inside the cache directory |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Error (the message is on stderr) |
| 124 | The command exceeded `SLACKCLI_COMMAND_TIMEOUT_MS` |
| 130 | Interrupted (Ctrl-C or SIGTERM) |

## Self-update

```bash
slackcli update --check   # only report
slackcli update           # download, verify the checksum, replace the binary
```

Only release binaries (installed with `install.sh` or downloaded by hand) replace themselves; for other installations the command fails and prints the command to run instead. slackcli also checks for a new release in the background, at most once every 24 hours, only when stderr is a terminal and never when `CI` is set; `SLACKCLI_NO_UPDATE_CHECK=1` disables it.

## Migrating from agent-slack

Automatic:

- **Credentials**: on the first run, when `~/.config/slackcli/credentials.json` does not exist, the workspaces in `~/.config/agent-slack/credentials.json` (and the `agent-slack` keychain service on macOS) are copied over. The old files are left untouched.
- **Environment variables**: `AGENT_SLACK_*` variables are still read when the `SLACKCLI_*` one is unset.

Changed:

- The binary is `slackcli`; environment variables use the `SLACKCLI_` prefix.
- Distribution: release binaries, `install.sh` and `go install` only. There is no npm package and no Nix flake any more.
- Downloads and caches moved from `~/.agent-slack` to the OS cache directory (see [Files and locations](#files-and-locations)).
- Safe mode also blocks `canvas create`, `canvas edit` and `workflow run`.
- `message send --attach a --attach b` posts one message with all the files. Message text may start with `- `.
- `message list --limit` goes up to 1000; a thread listing rejects history-only flags (`--limit`, `--oldest`, `--latest`, reaction filters). Numeric flags are validated everywhere.
- `search --channel` is repeatable or comma-separated (not space-separated); `--after`/`--before` are exclusive; `--no-download` is new; `more_messages`/`more_files` say how to continue. `search files` keeps files whose download failed, with an `error`.
- `message compose` accepts user-ID targets, prints `ts`, `permalink` and the `text` that was sent, and fails rather than print its link when no browser can be opened and stderr is not a terminal.
- `channel invite` reports `failed` targets and exits non-zero on partial failure.
- `auth whoami` shows `default` and `secrets` per workspace; `auth add --from-env` is new.
- `canvas get` reports `truncated`/`total_chars`; `canvas create/edit --file -` reads stdin; channel-canvas URLs are accepted.
- `workflow preview/get/run` accept shortcut URLs.
- `later list --cursor` pages; `later` write commands print `{ok, action, channel_id, ts}`; `unreads` adds `messages_truncated` and `messages_error`.

## Development

```bash
make build   # static binary in build/
make test    # go test ./...
make lint    # go vet + gofmt check
```

Releases are cut by pushing a `v*` tag; see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT, see [LICENSE](LICENSE). slackcli is derived from [stablyai/agent-slack](https://github.com/stablyai/agent-slack), copyright (c) 2026 Stably.
