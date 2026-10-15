import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Command } from "commander";
import type { CliContext } from "../src/cli/context.ts";
import { registerMessageCommand, registerThreadCommand } from "../src/cli/message-command.ts";
import { registerSearchCommand } from "../src/cli/search-command.ts";

const CHANNEL = "C0123456789";
const ROOT_TS = "1700000000.000001";
const REPLY_TS = "1700000000.000002";
const OTHER_TS = "1700000001.000000";

type ApiCall = { method: string; params: Record<string, unknown> };

function message(fields: { ts: string; text: string } & Record<string, unknown>) {
  return { user: "U11111111", ...fields };
}

function createClient(calls: ApiCall[]) {
  const root = message({
    ts: ROOT_TS,
    text: "root message with a long body",
    thread_ts: ROOT_TS,
    reply_count: 1,
  });
  const reply = message({ ts: REPLY_TS, text: "reply message", thread_ts: ROOT_TS });
  return {
    api: async (method: string, params: Record<string, unknown>) => {
      calls.push({ method, params });
      if (method === "conversations.history") {
        if (params.latest === ROOT_TS) {
          return { messages: [root] };
        }
        if (params.latest === REPLY_TS) {
          return { messages: [reply] };
        }
        return { messages: [message({ ts: OTHER_TS, text: "latest channel message" }), root] };
      }
      if (method === "conversations.replies") {
        return { messages: [root, reply] };
      }
      if (method === "chat.postMessage") {
        return { ts: OTHER_TS, channel: params.channel };
      }
      if (["chat.update", "chat.delete", "reactions.add"].includes(method)) {
        return {};
      }
      throw new Error(`Unexpected API method: ${method}`);
    },
  };
}

function createContext(calls: ApiCall[]): CliContext {
  const client = createClient(calls);
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
    errorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
  } as unknown as CliContext;
}

function buildProgram(calls: ApiCall[]): Command {
  const program = new Command();
  const ctx = createContext(calls);
  registerMessageCommand({ program, ctx });
  registerThreadCommand({ program, ctx });
  registerSearchCommand({ program, ctx });
  return program;
}

function findCommand(root: Command, ...path: string[]): Command {
  let current = root;
  for (const name of path) {
    const next = current.commands.find((command) => command.name() === name);
    if (!next) {
      throw new Error(`Missing command: ${path.join(" ")}`);
    }
    current = next;
  }
  return current;
}

describe("CLI aliases", () => {
  const originalXdg = process.env.XDG_RUNTIME_DIR;
  const originalSafeMode = process.env.AGENT_SLACK_SAFE_MODE;
  const originalLog = console.log;
  const originalError = console.error;
  let runtimeDir = "";
  let output: string[] = [];
  let errors: string[] = [];

  beforeEach(async () => {
    runtimeDir = await mkdtemp(join(tmpdir(), "agent-slack-alias-test-"));
    process.env.XDG_RUNTIME_DIR = runtimeDir;
    delete process.env.AGENT_SLACK_SAFE_MODE;
    output = [];
    errors = [];
    console.log = mock((line: string) => output.push(line)) as typeof console.log;
    console.error = mock((line: string) => errors.push(line)) as typeof console.error;
  });

  afterEach(async () => {
    process.env.XDG_RUNTIME_DIR = originalXdg;
    if (originalSafeMode === undefined) {
      delete process.env.AGENT_SLACK_SAFE_MODE;
    } else {
      process.env.AGENT_SLACK_SAFE_MODE = originalSafeMode;
    }
    console.log = originalLog;
    console.error = originalError;
    process.exitCode = 0;
    if (runtimeDir) {
      await rm(runtimeDir, { recursive: true, force: true });
    }
  });

  // oxlint-disable-next-line no-explicit-any -- payload shapes vary per command
  async function run(args: string[]): Promise<{ calls: ApiCall[]; payload: any }> {
    const calls: ApiCall[] = [];
    output = [];
    await buildProgram(calls).parseAsync(args, { from: "user" });
    expect(errors).toEqual([]);
    return { calls, payload: output.length ? JSON.parse(output.at(-1)!) : undefined };
  }

  async function runExpectingError(args: string[]): Promise<ApiCall[]> {
    const calls: ApiCall[] = [];
    await buildProgram(calls).parseAsync(args, { from: "user" });
    expect(process.exitCode).toBe(1);
    process.exitCode = 0;
    return calls;
  }

  const threadTimestamps = (payload: { messages: { ts: string }[] }) =>
    payload.messages.map((m) => m.ts);

  test("message list --channel reads channel history", async () => {
    const { calls, payload } = await run(["message", "list", "--channel", CHANNEL]);
    expect(calls[0]).toMatchObject({
      method: "conversations.history",
      params: { channel: CHANNEL },
    });
    expect(payload.channel_id).toBe(CHANNEL);
  });

  test("message list <channel> <reply ts> resolves to the whole thread", async () => {
    const { calls, payload } = await run(["message", "list", CHANNEL, REPLY_TS]);
    expect(calls.map((c) => c.method)).toEqual(["conversations.history", "conversations.replies"]);
    expect(calls[1]?.params.ts).toBe(ROOT_TS);
    expect(threadTimestamps(payload)).toEqual([ROOT_TS, REPLY_TS]);
  });

  test("message list rejects --channel that conflicts with the positional target", async () => {
    const calls = await runExpectingError(["message", "list", "C9999999999", "--channel", CHANNEL]);
    expect(errors[0]).toContain("Conflicting targets");
    expect(calls).toEqual([]);
  });

  test("message get accepts <channel> <ts> and --channel spellings", async () => {
    for (const args of [
      ["message", "get", CHANNEL, ROOT_TS],
      ["message", "get", "--channel", CHANNEL, "--ts", ROOT_TS],
      ["message", "get", "--channel", CHANNEL, ROOT_TS],
      ["message", CHANNEL, ROOT_TS],
    ]) {
      const { payload } = await run(args);
      expect(payload.message.ts).toBe(ROOT_TS);
      expect(payload.thread).toEqual({ ts: ROOT_TS, length: 2 });
    }
  });

  test("message get --thread returns the full thread", async () => {
    const { payload } = await run(["message", "get", CHANNEL, REPLY_TS, "--thread"]);
    expect(threadTimestamps(payload)).toEqual([ROOT_TS, REPLY_TS]);
  });

  test("--max-content-chars is accepted on message get/list as --max-body-chars", async () => {
    const { payload: got } = await run([
      "message",
      "get",
      CHANNEL,
      ROOT_TS,
      "--max-content-chars",
      "4",
    ]);
    expect(got.message.content).toBe("root\n…");

    const { payload: listed } = await run(["message", "list", CHANNEL, "--max-content-chars", "4"]);
    expect(listed.messages.map((m: { content: string }) => m.content)).toEqual([
      "root\n…",
      "late\n…",
    ]);
  });

  test("thread [get] aliases message list in thread mode", async () => {
    for (const args of [
      ["thread", CHANNEL, REPLY_TS],
      ["thread", "get", CHANNEL, REPLY_TS],
      ["thread", "get", "--channel", CHANNEL, "--ts", REPLY_TS],
    ]) {
      const { payload } = await run(args);
      expect(threadTimestamps(payload)).toEqual([ROOT_TS, REPLY_TS]);
    }
  });

  test("thread without a ts errors instead of listing channel history", async () => {
    const calls = await runExpectingError(["thread", CHANNEL]);
    expect(errors[0]).toContain("thread needs a Slack message URL");
    expect(calls).toEqual([]);
  });

  test("thread is hidden from top-level help", () => {
    const help = buildProgram([]).helpInformation();
    expect(help).toContain("message");
    expect(help).not.toMatch(/^\s+thread\b/m);
  });

  test("message send accepts --channel and --text", async () => {
    for (const args of [
      ["message", "send", "--channel", CHANNEL, "--text", "hello"],
      ["message", "send", "--channel", CHANNEL, "hello"],
      ["message", "send", CHANNEL, "--text", "hello"],
    ]) {
      const { calls } = await run(args);
      expect(calls).toHaveLength(1);
      expect(calls[0]).toMatchObject({
        method: "chat.postMessage",
        params: { channel: CHANNEL, text: "hello" },
      });
    }
  });

  test("message send refuses ambiguous --channel/--text combinations without calling Slack", async () => {
    const conflicting = await runExpectingError([
      "message",
      "send",
      "C9999999999",
      "--channel",
      CHANNEL,
    ]);
    expect(errors[0]).toContain("Conflicting targets");
    expect(conflicting).toEqual([]);

    errors.length = 0;
    const twoTexts = await runExpectingError([
      "message",
      "send",
      CHANNEL,
      "hello",
      "--text",
      "bye",
    ]);
    expect(errors[0]).toContain("Conflicting values for [text] and --text");
    expect(twoTexts).toEqual([]);
  });

  test("message edit, delete and react accept --channel", async () => {
    const { calls: edit } = await run([
      "message",
      "edit",
      "--channel",
      CHANNEL,
      "--ts",
      ROOT_TS,
      "--text",
      "fixed",
    ]);
    expect(edit[0]).toMatchObject({
      method: "chat.update",
      params: { channel: CHANNEL, ts: ROOT_TS, text: "fixed" },
    });

    const { calls: del } = await run(["message", "delete", "--channel", CHANNEL, "--ts", ROOT_TS]);
    expect(del[0]).toMatchObject({
      method: "chat.delete",
      params: { channel: CHANNEL, ts: ROOT_TS },
    });

    const { calls: react } = await run([
      "message",
      "react",
      "add",
      "--channel",
      CHANNEL,
      "--ts",
      ROOT_TS,
      "eyes",
    ]);
    expect(react[0]).toMatchObject({
      method: "reactions.add",
      params: { channel: CHANNEL, timestamp: ROOT_TS, name: "eyes" },
    });
  });

  test("message edit still requires text", async () => {
    const calls = await runExpectingError(["message", "edit", CHANNEL, "--ts", ROOT_TS]);
    expect(errors[0]).toContain("Missing <text>");
    expect(calls).toEqual([]);
  });

  test("search defaults to messages and accepts a hidden --max-body-chars", async () => {
    const program = buildProgram([]);
    const messagesCmd = findCommand(program, "search", "messages");
    let ran: { query?: string; maxContentChars?: string } = {};
    messagesCmd.action((query: string, options: { maxContentChars?: string }) => {
      ran = { query, maxContentChars: options.maxContentChars };
    });
    await program.parseAsync(["search", "some query", "--max-body-chars", "30"], { from: "user" });
    expect(ran).toEqual({ query: "some query", maxContentChars: "30" });
    expect(messagesCmd.helpInformation()).not.toContain("--max-body-chars");
  });

  test("search files still routes to the files subcommand", async () => {
    const program = buildProgram([]);
    let ran = "";
    findCommand(program, "search", "files").action((query: string) => {
      ran = query;
    });
    await program.parseAsync(["search", "files", "report"], { from: "user" });
    expect(ran).toBe("report");
  });
});
