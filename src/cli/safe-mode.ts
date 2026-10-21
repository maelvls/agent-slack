import type { Command } from "commander";
import type { CliContext } from "./context.ts";
import { composeMessage } from "./compose-actions.ts";

const TRUTHY_VALUES = new Set(["1", "true", "yes", "on"]);

/**
 * Safe mode keeps a human in the loop for anything other people can see:
 * `message send` is redirected to the draft editor, and every other command
 * with a visible effect calls `refuseUnderSafeMode`. Enabled via the global
 * `--safe-mode` flag or the AGENT_SLACK_SAFE_MODE env var ("1", "true",
 * "yes", "on").
 */
export function isSafeModeEnabled(input?: {
  cliFlag?: boolean;
  env?: Record<string, string | undefined>;
}): boolean {
  if (input?.cliFlag) {
    return true;
  }
  const env = input?.env ?? process.env;
  const raw = env.AGENT_SLACK_SAFE_MODE?.trim().toLowerCase();
  return raw !== undefined && TRUTHY_VALUES.has(raw);
}

export function safeModeBlockedError(commandName: string): Error {
  return new Error(
    `Safe mode is active (AGENT_SLACK_SAFE_MODE or --safe-mode): "${commandName}" is blocked so an agent cannot change what other people see in Slack without human review. Disable safe mode to run it.`,
  );
}

/**
 * Throws when safe mode is on. Call it first in the action of any command
 * whose effect other people can see and that has no draft-editor review
 * step, so the refusal happens before any Slack API call. `command` may be
 * any command in the tree; the global `--safe-mode` flag is read from it.
 */
export function refuseUnderSafeMode(command: Command, commandName: string): void {
  if (isSafeModeEnabled({ cliFlag: Boolean(command.optsWithGlobals().safeMode) })) {
    throw safeModeBlockedError(commandName);
  }
}

export type SendOptionsForRedirect = {
  workspace?: string;
  threadTs?: string;
  attach?: string[];
  blocks?: string;
  replyBroadcast?: boolean;
  schedule?: string;
  scheduleIn?: string;
  unfurl?: boolean;
};

/**
 * Redirects `message send` to the interactive draft editor so a human
 * reviews and sends the message. Flags the draft editor cannot represent
 * (attachments, raw blocks, scheduling, broadcasts) are rejected instead
 * of being silently dropped.
 */
export async function redirectSendToDraft(
  input: {
    ctx: CliContext;
    targetInput: string;
    text: string;
    options: SendOptionsForRedirect;
  },
  draftFn: typeof composeMessage = composeMessage,
): Promise<Record<string, unknown>> {
  const unsupported: string[] = [];
  if ((input.options.attach ?? []).length > 0) {
    unsupported.push("--attach");
  }
  if (input.options.blocks !== undefined) {
    unsupported.push("--blocks");
  }
  if (input.options.schedule !== undefined) {
    unsupported.push("--schedule");
  }
  if (input.options.scheduleIn !== undefined) {
    unsupported.push("--schedule-in");
  }
  if (input.options.replyBroadcast) {
    unsupported.push("--reply-broadcast");
  }
  if (unsupported.length > 0) {
    throw new Error(
      `Safe mode is active: "message send" is redirected to the draft editor, which does not support ${unsupported.join(", ")}. Drop those flags or disable safe mode.`,
    );
  }
  if (process.env.CI) {
    throw new Error(
      "Safe mode is active but the interactive draft editor is unavailable in CI. Disable safe mode (or unset CI) to send messages.",
    );
  }

  console.error(
    '⚠ Safe mode active: redirecting "message send" → draft editor. Nothing posts until a human sends it from the editor.',
  );

  const payload = await draftFn({
    ctx: input.ctx,
    targetInput: input.targetInput,
    initialText: input.text,
    options: {
      workspace: input.options.workspace,
      threadTs: input.options.threadTs,
      unfurl: input.options.unfurl,
    },
  });
  return { safe_mode: true, redirected_from: "send", ...payload };
}
