import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Command } from "commander";
import type { CliContext } from "../src/cli/context.ts";
import { registerMessageCommand } from "../src/cli/message-command.ts";
import { registerSearchCommand } from "../src/cli/search-command.ts";

type ApiHandler = (method: string, params: Record<string, unknown>) => unknown;

function createContext(handler: ApiHandler): CliContext {
  const client = {
    api: async (method: string, params: Record<string, unknown>) => handler(method, params),
  };
  return {
    effectiveWorkspaceUrl: (flag?: string) => flag,
    assertWorkspaceSpecifiedForChannelNames: async () => {},
    withAutoRefresh: async <T>(input: {
      workspaceUrl: string | undefined;
      work: () => Promise<T>;
    }) => input.work(),
    getClientForWorkspace: async () => ({
      client: client as never,
      auth: { auth_type: "standard", token: "x" },
      workspace_url: "https://workspace.slack.com",
    }),
    normalizeUrl: (u: string) => u,
    errorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
    parseContentType: () => "any",
    parseCurl: () => ({
      workspace_url: "https://workspace.slack.com",
      xoxc_token: "xoxc-1",
      xoxd_cookie: "xoxd-1",
    }),
    importDesktop: async () => ({
      cookie_d: "",
      teams: [],
      source: { leveldb_path: "", cookies_path: "" },
    }),
    importChrome: () => ({ cookie_d: "", teams: [] }),
    importBrave: async () => null,
    importFirefox: async () => null,
  };
}

const FAILING_FILE = {
  id: "F0C0ZKD6QF5",
  name: "secret.txt",
  title: "secret.txt",
  mimetype: "text/plain",
  url_private: "https://files.slack.com/files/F0C0ZKD6QF5",
};

describe("JSON output of read commands", () => {
  const originalXdg = process.env.XDG_RUNTIME_DIR;
  const originalLog = console.log;
  const originalError = console.error;
  const originalWarn = console.warn;
  const originalFetch = globalThis.fetch;
  let runtimeDir = "";
  let stdout: string[] = [];
  let stderr: unknown[][] = [];

  beforeEach(async () => {
    runtimeDir = await mkdtemp(join(tmpdir(), "agent-slack-json-output-test-"));
    process.env.XDG_RUNTIME_DIR = runtimeDir;
    stdout = [];
    stderr = [];
    console.log = mock((line: string) => {
      stdout.push(line);
    }) as typeof console.log;
    console.error = mock((...args: unknown[]) => {
      stderr.push(args);
    }) as typeof console.error;
    console.warn = mock((...args: unknown[]) => {
      stderr.push(args);
    }) as typeof console.warn;
  });

  afterEach(async () => {
    process.env.XDG_RUNTIME_DIR = originalXdg;
    console.log = originalLog;
    console.error = originalError;
    console.warn = originalWarn;
    globalThis.fetch = originalFetch;
    if (runtimeDir) {
      await rm(runtimeDir, { recursive: true, force: true });
    }
  });

  function mockFetchStatus(status: number) {
    const mocked = mock(() => Promise.resolve(new Response(null, { status })));
    globalThis.fetch = mocked as unknown as typeof globalThis.fetch;
    return mocked;
  }

  async function run(input: {
    register: (input: { program: Command; ctx: CliContext }) => void;
    handler: ApiHandler;
    argv: string[];
  }): Promise<unknown> {
    const { register, handler, argv } = input;
    const program = new Command();
    register({ program, ctx: createContext(handler) });
    await program.parseAsync(argv, { from: "user" });
    expect(stderr).toEqual([]);
    expect(stdout).toHaveLength(1);
    return JSON.parse(stdout[0]!);
  }

  test("search messages with no hits prints an empty messages array", async () => {
    const out = await run({
      register: registerSearchCommand,
      handler: (method) => {
        if (method === "search.messages") {
          return { messages: { matches: [] } };
        }
        throw new Error(`Unexpected API method: ${method}`);
      },
      argv: ["search", "messages", "zzqqxxnotathing"],
    });
    expect(out).toEqual({ messages: [] });
  });

  test("search all with no hits prints empty messages and files arrays", async () => {
    const out = await run({
      register: registerSearchCommand,
      handler: (method) => {
        if (method === "search.messages") {
          return { messages: { matches: [] } };
        }
        if (method === "search.files") {
          return { files: { matches: [] } };
        }
        throw new Error(`Unexpected API method: ${method}`);
      },
      argv: ["search", "all", "zzqqxxnotathing"],
    });
    expect(out).toEqual({ messages: [], files: [] });
  });

  test("search files reports failed downloads in file_download_errors, not on stderr", async () => {
    mockFetchStatus(401);
    const out = await run({
      register: registerSearchCommand,
      handler: (method) => {
        if (method === "search.files") {
          return { files: { matches: [FAILING_FILE] } };
        }
        throw new Error(`Unexpected API method: ${method}`);
      },
      argv: ["search", "files", "secret"],
    });
    expect(out).toEqual({
      files: [],
      file_download_errors: [{ file_id: "F0C0ZKD6QF5", error: "Failed to download file (401)" }],
    });
  });

  test("message list on an empty channel keeps the messages array", async () => {
    const out = await run({
      register: registerMessageCommand,
      handler: (method) => {
        if (method === "conversations.history") {
          return { messages: [] };
        }
        throw new Error(`Unexpected API method: ${method}`);
      },
      argv: ["message", "list", "D12345678"],
    });
    expect(out).toEqual({ channel_id: "D12345678", messages: [] });
  });

  test("message list records file download failures on the file, not on stderr", async () => {
    const fetchMock = mockFetchStatus(401);
    const out = (await run({
      register: registerMessageCommand,
      handler: (method) => {
        if (method === "conversations.history") {
          return {
            messages: [
              { ts: "1.000001", text: "see attached", user: "U11111111", files: [FAILING_FILE] },
            ],
          };
        }
        throw new Error(`Unexpected API method: ${method}`);
      },
      argv: ["message", "list", "C12345678"],
    })) as { messages: { files?: unknown[] }[] };
    expect(fetchMock).toHaveBeenCalled();
    expect(out.messages[0]!.files).toEqual([
      {
        name: "secret.txt",
        mimetype: "text/plain",
        error: "Failed to download file (401)",
        path: join(runtimeDir, "agent-slack", "tmp", "downloads", "F0C0ZKD6QF5.download-error.txt"),
      },
    ]);
  });
});
