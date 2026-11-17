# AGENTS.md

slackcli is a Slack automation CLI for AI agents, written in Go (module `github.com/maelvls/slackcli`). Its users are mostly agents: `--help` text and error messages are the product's documentation.

## Commands to run

```bash
make build   # static binary in build/
make test    # go test ./...   (narrow with PKGS=./internal/slack/search/...)
make lint    # go vet + gofmt check; `make fmt` fixes formatting
```

CI also runs `go test -race ./...`, `go mod tidy -diff`, a cross-compile of every release target and `shellcheck install.sh`.

Never test against real Slack: do not run the built binary against a workspace, read `~/.config/slackcli`, or write to the keychain. Use fakes and `t.TempDir()`. Running `--help` is always safe.

## Layout

| Path | Role |
| --- | --- |
| `cmd/slackcli` | `main`: calls `root.Run` |
| `internal/appinfo` | The tool's identity: `Name`, `EnvPrefix`, `Repo`, `Version`, `Env`/`EnvName`/`EnvBool`/`EnvDurationMS`, `ConfigDir`, `CacheDir`, `DownloadsDir` |
| `internal/slackapi` | Slack Web API client (`API`, `APIMultipart`, `Authorize`), `*APIError`, `IsErrorCode`, `IsAuthError`, `*RateLimitError`, `NewFake` |
| `internal/auth` | Credentials store (`credentials.json` + macOS keychain), workspace selection, legacy migration |
| `internal/authextract` | Credential extraction from Slack Desktop, Chrome, Brave, Firefox |
| `internal/clictx` | Runtime shared by commands: `*Context` (`Client`, `EffectiveWorkspace`, `Print`, `Hintf`, `SafeMode`, `DisableTimeout`), `WithAutoRefresh`, `AddWorkspaceFlag`, `NoTimeout` |
| `internal/cmd/root` | Command tree, `Run`, command-timeout watchdog, exit codes |
| `internal/cmd/<name>cmd` | One package per command, exporting `New(rt *clictx.Context) *cobra.Command` |
| `internal/slack/<domain>` | Slack logic without CLI concerns (`messages`, `search`, `channels`, `users`, `files`, `upload`, `canvas`, `drafts`, `later`, `unreads`, `scheduled`, `workflows`, `slacktext`, `slackurl`, `enrich` (the rule for optional lookups: degrade on rate limits), ...) |
| `internal/output` | Pruned, key-order-preserving, 2-space JSON |
| `internal/jsonx` | Accessors for loosely-typed JSON (`Map`, `Slice`, `Str`, `Int`, `Bool`, ...) |
| `internal/slackid` | Recognises channel and user IDs |
| `internal/update` | Self-update and the background update notice |

## Conventions

- **Name only via `appinfo`**: never write the tool's name, env prefix or repo in Go code. Use `appinfo.Name`, `appinfo.EnvName("FOO")`, `appinfo.Env("FOO")` (which also reads the legacy prefix), `appinfo.CacheDir()`. `SLACK_TOKEN`, `SLACK_COOKIE_D` and `SLACK_WORKSPACE_URL` keep their names. In docs, write the name literally and in full, so that a search-and-replace can rename it.
- **Errors are returned from `RunE`**: the root prints them and sets the exit code. No `os.Exit`, no printing the error yourself, no `log.Fatal`/`panic` for runtime failures. Wrap with `fmt.Errorf("doing X: %w", err)` and make messages actionable: an agent reads them verbatim, so say what to run or pass instead.
- **Results go through `rt.Print(v)`** (pruned JSON on stdout); hints and warnings through `rt.Hintf` (stderr).
- **Never string-match Slack errors**: a Slack `ok:false` is an `*slackapi.APIError`; test it with `slackapi.IsErrorCode(err, "channel_not_found")`.
- **Clients come from `rt.Client(ctx, workspace)` inside `clictx.WithAutoRefresh`**, so that expired credentials are re-imported and the work retried once.
- **Credentials only go to Slack hosts**: use `client.Authorize(req)` only for requests to Slack-owned hosts; never follow a URL from a payload with credentials attached. Keep secrets off argv and out of logs and errors.
- **Commands**: use `RunE`, `cmd.Context()` and cobra arg validators. Do not set `PersistentPreRun*`, `SilenceUsage` or `SilenceErrors` (the root does). Validate numeric and enum flags and refuse bad values rather than clamping silently. Mark commands that wait on a human with `clictx.NoTimeout`.
- **Safe mode**: a new command that posts, edits or deletes content must check `rt.SafeMode()` and refuse (or hand over to the compose editor). Say in its help whether safe mode covers it.
- **Help text is a contract**: `Short`/`Long` and flag usage must state the arguments, the JSON printed and the side effects. Command names, flags, defaults and JSON field names are a public surface: do not change them casually.
- **Output types**: structs with `json:"snake_case"` tags; field order is output order. `output` prunes null/blank/empty values but keeps `false` and `0`.
- **I/O functions take `ctx context.Context` first.** No global mutable state except mutex-guarded caches; make clocks, sleeps and directories injectable for tests.
- **Tests**: standard `testing` only (no assertion libraries). Fake the API with `slackapi.NewFake(handler)`, raw HTTP with `net/http/httptest`; use `t.TempDir()` and `t.Setenv()`. Add a test for every bug fixed.
- **Dependencies**: prefer the standard library; do not add a module without a strong reason. Builds are `CGO_ENABLED=0`.
- **Style**: idiomatic Go, small files grouped by concern, doc comments on exported identifiers, comments that explain why, no dead code or speculative abstractions.

## Release naming contract

`.goreleaser.yaml`, `install.sh` and `internal/update` agree on `slackcli_<os>_<arch>.tar.gz` (`.zip` on Windows), the binary at the archive root, and `checksums.txt`. `internal/update`'s tooling test fails if they drift; change all three together.

## Docs and skill

`README.md` is for humans; the installable agent skill is `skills/slackcli/SKILL.md` with `skills/slackcli/references/`. Keep both aligned with the code whenever a command, flag, output field, environment variable or safety behaviour changes.

- Write the skill for an agent executing Slack tasks, not as user-facing documentation.
- Keep it token-efficient: no table of contents, no exhaustive command/flag catalogue, no duplication of the README or the CLI help.
- Treat the installed `slackcli --help` and subcommand help as authoritative. Keep only non-obvious workflow, safety, targeting and output behaviour in the skill.
