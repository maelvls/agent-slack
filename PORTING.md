# Porting guide (temporary — deleted when the rewrite lands)

We are rewriting `agent-slack` (TypeScript + Bun, in `src/` and `test/`) in Go,
under a new name. The working name is **slackcli**; it may still change, so
**never hard-code the name**: use `appinfo.Name`, `appinfo.EnvName(...)`,
`appinfo.Env(...)` in every user-visible string, env lookup and path.

Several agents work in this same checkout at the same time. The rules below
exist so that nobody breaks anybody else's build.

## Ground rules

1. **Stay in your lane.** Only create/modify files in the packages your brief
   assigns to you. Never edit another package, `go.mod`/`go.sum`, `PORTING.md`,
   or anything under `src/` / `test/` (the TypeScript is read-only reference
   until the very end). If you need a change in a package you do not own, do
   not make it: describe it precisely in your final report.
2. **No git.** Do not commit, stash, branch, reset or checkout. The integrator
   commits.
3. **No `go mod tidy`, no `go get`.** Available modules (already in `go.mod`):
   `github.com/spf13/cobra`, `github.com/golang/snappy`, `modernc.org/sqlite`
   (pure-Go SQLite, driver name `"sqlite"`),
   `github.com/JohannesKaufmann/html-to-markdown/v2`, `golang.org/x/net`,
   `golang.org/x/sys`. Prefer the standard library (Go 1.27: `crypto/pbkdf2`,
   `slices`, `maps`, `iter`, `log/slog`, `testing/synctest` … are all there).
   If you truly need another module, say so in your report instead.
4. **Build and test only your own packages**: `go build ./internal/yourpkg/...`,
   `go vet ./internal/yourpkg/...`, `go test ./internal/yourpkg/...`. Other
   packages may be mid-edit and broken; that is not your problem, and
   `go build ./...` failing elsewhere is expected. Run `gofmt -l` on your files.
5. **Never touch real Slack or real credentials.** Do not run the built binary
   against the user's workspaces, do not read `~/.config/agent-slack`, do not
   write to the macOS keychain. Unit tests only, with fakes and temp dirs.
   (The auth-extraction brief has one explicit, read-only exception.)
6. Do not write files outside this checkout, except scratch files under the
   directory named by `$CLAUDE_JOB_DIR/tmp` if set (else `os.MkdirTemp`).

## Foundation you build on (read these files first)

| Package | What it gives you |
| --- | --- |
| `internal/appinfo` | `Name`, `Version`, `Repo`, `UserAgent()`, `Env`, `EnvName`, `EnvBool`, `EnvDurationMS`, `ConfigDir`, `CacheDir`, `DownloadsDir`, `EnsureDownloadsDir` |
| `internal/slackapi` | `Client` (`API`, `APIMultipart`, `Auth`, `WorkspaceURL`, `HTTPClient`, `Authorize`), `Auth`, `*APIError`, `ErrorCode`, `IsErrorCode`, `IsAuthError`, `*RateLimitError`, `NewFake` + `WithHandler`/`WithHTTPClient`/`WithBaseURL` for tests |
| `internal/jsonx` | accessors for loosely-typed JSON: `Map`, `Slice`, `Maps`, `Str`, `Num`, `Int`, `Bool`, `Get`, `GetStr`, `Stringish` … |
| `internal/output` | `Marshal` / `Write`: pruned (no null/blank/empty), key-order-preserving, 2-space JSON |
| `internal/slackid` | `IsChannelID`, `IsUserID`, `ParseChannelInput` |
| `internal/auth` | credentials `Store` (`Load`, `Resolve`, `Upsert`, `SetDefault`, `Remove`), `Workspace`, `NormalizeURL`, `SelectWorkspace` |
| `internal/authextract` | `Extracted`, `Team`, `Desktop`/`Chrome`/`Brave`/`Firefox` extractors |
| `internal/clictx` | `*Context` (`Client(ctx, workspace)`, `EffectiveWorkspace`, `AssertWorkspaceForChannelNames`, `Print`, `Hintf`, `SafeMode`, `DisableTimeout`, `Import`), `WithAutoRefresh`, `AddWorkspaceFlag`, `NoTimeout` |
| `internal/cmd/root` | command tree + `Run`; global timeout watchdog; prints the returned error and sets the exit code |

How the TypeScript maps onto them:

- `client.api(method, params)` → `client.API(ctx, method, map[string]any{...})`.
  A Slack `ok:false` is an `*slackapi.APIError`; test for codes with
  `slackapi.IsErrorCode(err, "channel_not_found")`, **never** by matching the
  error string.
- `isRecord` / `getString` / `asArray` → `jsonx.Map` / `jsonx.Str` / `jsonx.Slice`.
- `console.log(JSON.stringify(pruneEmpty(x), null, 2))` → `return rt.Print(x)`.
- `console.error(hint)` → `rt.Hintf(...)`.
- `ctx.withAutoRefresh({work})` + `ctx.getClientForWorkspace(ws)` →
  ```go
  payload, err := clictx.WithAutoRefresh(ctx, rt, func() (T, error) {
      r, err := rt.Client(ctx, workspace)
      if err != nil { return zero, err }
      return doTheThing(ctx, r.Client, ...)
  })
  ```
- `catch (err) { console.error(errorMessage(err)); process.exitCode = 1 }` →
  just `return err` from `RunE`. Never call `os.Exit`, never print the error
  yourself, never use `log.Fatal`/`panic` for runtime failures.
- `process.env.AGENT_SLACK_FOO` → `appinfo.Env("FOO")` (reads `SLACKCLI_FOO`,
  falls back to `AGENT_SLACK_FOO`). `SLACK_TOKEN`, `SLACK_COOKIE_D`,
  `SLACK_WORKSPACE_URL` keep their names.
- `getAppDir()` / `ensureDownloadsDir()` → `appinfo.CacheDir()` /
  `appinfo.EnsureDownloadsDir()`.

## Conventions

- **Commands**: one package per top-level command, `internal/cmd/<name>cmd`,
  exporting `func New(rt *clictx.Context) *cobra.Command`. Each such package
  currently holds a `stub.go` placeholder: delete it when you write the real
  one. Use `RunE`, `cmd.Context()`, `cobra` arg validators
  (`cobra.ExactArgs(1)` …). Do **not** set `PersistentPreRun*` hooks, and do
  not set `SilenceUsage`/`SilenceErrors` (the root does). Write flag help and
  `Short`/`Long` text carefully: `--help` is the tool's documentation for
  agents.
- **CLI surface is a contract**: keep command names, argument order, flag
  names, defaults and JSON output field names identical to the TypeScript
  unless something is actually broken. List every intentional difference in
  your report.
- **Output types**: prefer structs with `json:"snake_case"` tags declared in
  the same field order the TypeScript object literal used (output keeps
  struct order). `output` prunes empties, so `omitempty` is optional; but a
  field that must be able to say `false` or `0` is kept either way.
  Pass-through Slack payloads can stay `map[string]any`.
- **Context**: every function that does I/O takes `ctx context.Context` first.
- **Errors**: `fmt.Errorf("doing X: %w", err)`; messages are shown verbatim to
  an agent, so make them actionable (say what to run or pass instead).
- **No global mutable state** except caches guarded by a mutex. Make clocks,
  sleeps and directories injectable where tests need it.
- **Tests**: port **every** TypeScript test that covers your files into Go
  (`testing` only — no testify), plus tests for whatever you fix. Table tests
  where it helps. Use `slackapi.NewFake(handler)` to fake the API,
  `net/http/httptest` for raw HTTP, `t.TempDir()`, `t.Setenv()`.
- **Style**: idiomatic Go, small files grouped by concern, doc comments on
  exported identifiers, no dead code, no speculative abstractions. Comments
  explain *why*, not *what*.

## "Fix the problems you find"

The owner asked for more than a transliteration: **fix every problem you find
along the way.** While reading the TypeScript, look for and fix in the Go
version:

- bugs and edge cases (wrong pagination, off-by-one, timezones, races,
  unhandled Slack responses, stale names after the `draft`→`compose` rename,
  flags that are parsed but ignored, validation that accepts garbage like
  `--limit abc` → `NaN`);
- security problems (secrets on argv or in logs, path traversal in download
  file names, predictable temp files, over-broad file modes, credentials sent
  to non-Slack hosts, HTML/JS injection, unbounded reads);
- performance problems (O(workspace) API pagination where one call would do,
  serial calls that could be concurrent, repeated work);
- UX problems for the agent using the tool (misleading errors, silent
  truncation, output that hides a failure).

Do not replicate a bug for fidelity. Do not invent features either. When in
doubt about intent, keep the behaviour and flag it in your report.

## Final report (your last message)

Reply with exactly these sections, tersely:

1. **Files** — what you created/deleted.
2. **Exported API** — the Go signatures other packages will call.
3. **Problems fixed** — one line each: `<ts file>: <problem> → <fix>`.
4. **Intentional differences** from the TypeScript CLI surface/output.
5. **Needs from others** — changes you need in packages you do not own.
6. **Verification** — exact commands you ran and their result; anything not
   tested and why.
