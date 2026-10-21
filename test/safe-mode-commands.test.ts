import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { Command } from "commander";
import type { CliContext } from "../src/cli/context.ts";
import { registerCanvasCommand } from "../src/cli/canvas-command.ts";
import { registerChannelCommand } from "../src/cli/channel-command.ts";
import { registerMessageCommand } from "../src/cli/message-command.ts";
import { registerWorkflowCommand } from "../src/cli/workflow-command.ts";
import { withEnvironment } from "./helpers/environment.ts";

/** A context that records every call that could lead to a Slack API request. */
function createRecordingContext() {
  const calls: string[] = [];
  const client = {
    api: async (method: string) => {
      calls.push(`api:${method}`);
      throw new Error(`stub: ${method}`);
    },
  };
  const ctx = {
    effectiveWorkspaceUrl: (flag?: string) => flag,
    assertWorkspaceSpecifiedForChannelNames: async () => {
      calls.push("assertWorkspaceSpecifiedForChannelNames");
    },
    withAutoRefresh: async <T>(input: { work: () => Promise<T> }) => {
      calls.push("withAutoRefresh");
      return input.work();
    },
    getClientForWorkspace: async () => {
      calls.push("getClientForWorkspace");
      return { client: client as never, auth: { auth_type: "standard", token: "x" } };
    },
    normalizeUrl: (u: string) => u,
    errorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
  } as unknown as CliContext;
  return { ctx, calls };
}

function createProgram(ctx: CliContext): Command {
  const program = new Command().option("--safe-mode").exitOverride();
  registerMessageCommand({ program, ctx });
  registerCanvasCommand({ program, ctx });
  registerChannelCommand({ program, ctx });
  registerWorkflowCommand({ program, ctx });
  return program;
}

const GUARDED_COMMANDS: { name: string; argv: string[] }[] = [
  { name: "workflow run", argv: ["workflow", "run", "Ft123", "--channel", "C123"] },
  { name: "channel new", argv: ["channel", "new", "--name", "proj-x"] },
  { name: "channel invite", argv: ["channel", "invite", "--channel", "C123", "--users", "U123"] },
  {
    name: "channel invite",
    argv: ["channel", "invite", "--channel", "C123", "--users", "a@example.com", "--external"],
  },
  { name: "canvas edit", argv: ["canvas", "edit", "F0123456789", "--markdown", "# Hi"] },
  {
    name: "canvas create --channel",
    argv: ["canvas", "create", "--markdown", "# Hi", "--channel", "C123"],
  },
  {
    name: "message scheduled cancel",
    argv: ["message", "scheduled", "cancel", "Q123", "--channel", "C123"],
  },
  { name: "message react add", argv: ["message", "react", "add", "C123", "rocket", "--ts", "1.2"] },
  {
    name: "message react remove",
    argv: ["message", "react", "remove", "C123", "rocket", "--ts", "1.2"],
  },
  { name: "message edit", argv: ["message", "edit", "C123", "new text", "--ts", "1.2"] },
  { name: "message delete", argv: ["message", "delete", "C123", "--ts", "1.2"] },
];

describe("safe mode refuses commands visible to other people", () => {
  const originalLog = console.log;
  const originalError = console.error;
  let errors: string[];

  beforeEach(() => {
    process.exitCode = 0;
    errors = [];
    console.log = () => {};
    console.error = (...args: unknown[]) => {
      errors.push(args.join(" "));
    };
  });

  afterEach(() => {
    process.exitCode = 0;
    console.log = originalLog;
    console.error = originalError;
  });

  test.each(GUARDED_COMMANDS)(
    "$name exits 1 under AGENT_SLACK_SAFE_MODE=1",
    async ({ name, argv }) => {
      const { ctx, calls } = createRecordingContext();
      await withEnvironment({ AGENT_SLACK_SAFE_MODE: "1" }, async () => {
        await createProgram(ctx).parseAsync(argv, { from: "user" });
      });
      expect(process.exitCode).toBe(1);
      expect(calls).toEqual([]);
      expect(errors.join("\n")).toContain(`"${name}" is blocked`);
    },
  );

  test.each(GUARDED_COMMANDS)("$name exits 1 under --safe-mode", async ({ name, argv }) => {
    const { ctx, calls } = createRecordingContext();
    await withEnvironment({ AGENT_SLACK_SAFE_MODE: undefined }, async () => {
      await createProgram(ctx).parseAsync(["--safe-mode", ...argv], { from: "user" });
    });
    expect(process.exitCode).toBe(1);
    expect(calls).toEqual([]);
    expect(errors.join("\n")).toContain(`"${name}" is blocked`);
  });

  test.each(GUARDED_COMMANDS)(
    "$name reaches Slack when safe mode is off",
    async ({ name, argv }) => {
      const { ctx, calls } = createRecordingContext();
      await withEnvironment({ AGENT_SLACK_SAFE_MODE: undefined }, async () => {
        await createProgram(ctx).parseAsync(argv, { from: "user" });
      });
      expect(calls).toContain("getClientForWorkspace");
      expect(errors.join("\n")).not.toContain(`"${name}" is blocked`);
    },
  );

  test("standalone canvas create stays allowed (private to its creator)", async () => {
    const { ctx, calls } = createRecordingContext();
    await withEnvironment({ AGENT_SLACK_SAFE_MODE: "1" }, async () => {
      await createProgram(ctx).parseAsync(["canvas", "create", "--markdown", "# Hi"], {
        from: "user",
      });
    });
    expect(calls).toContain("getClientForWorkspace");
    expect(errors.join("\n")).not.toContain("is blocked");
  });

  test("channel mark stays allowed (read state is personal)", async () => {
    const { ctx, calls } = createRecordingContext();
    await withEnvironment({ AGENT_SLACK_SAFE_MODE: "1" }, async () => {
      await createProgram(ctx).parseAsync(["channel", "mark", "C123", "--ts", "1.2"], {
        from: "user",
      });
    });
    expect(calls).toContain("getClientForWorkspace");
    expect(errors.join("\n")).not.toContain("is blocked");
  });
});
