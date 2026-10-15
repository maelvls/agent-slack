import { parseSlackMessageUrl, type SlackMessageRef } from "../slack/url.ts";
import { isChannelId } from "../slack/channels.ts";
import { isUserId } from "../slack/user-id.ts";

export type MsgTarget =
  | { kind: "url"; ref: SlackMessageRef }
  | { kind: "channel"; channel: string }
  | { kind: "user"; userId: string };

export function parseMsgTarget(input: string): MsgTarget {
  const trimmed = input.trim();
  if (!trimmed) {
    throw new Error("Missing target");
  }

  try {
    const ref = parseSlackMessageUrl(trimmed);
    return { kind: "url", ref };
  } catch {
    // not a slack message URL
  }

  if (isUserId(trimmed)) {
    return { kind: "user", userId: trimmed };
  }
  if (trimmed.startsWith("#")) {
    return { kind: "channel", channel: trimmed };
  }
  if (isChannelId(trimmed)) {
    return { kind: "channel", channel: trimmed };
  }

  // Allow bare channel names ("general") for convenience.
  return { kind: "channel", channel: `#${trimmed}` };
}

export function isSlackTs(input: string): boolean {
  return /^\d+\.\d+$/.test(input.trim());
}

function sameTarget(a: string, b: string): boolean {
  return a.trim().replace(/^#/, "") === b.trim().replace(/^#/, "");
}

// Unlike parseMsgTarget, bare words don't count: they may be message text or an emoji.
function looksLikeExplicitTarget(input: string): boolean {
  const trimmed = input.trim();
  try {
    parseSlackMessageUrl(trimmed);
    return true;
  } catch {
    // not a slack message URL
  }
  return (
    isChannelId(trimmed) || isUserId(trimmed) || (trimmed.startsWith("#") && !/\s/.test(trimmed))
  );
}

/**
 * Reconcile the positional `<target>` with `--channel`. `positionals` holds every
 * declared positional (`[target, ...rest]`, unfilled ones undefined). When
 * `--channel` supplies the target, a lone differing positional belongs to the
 * next slot (`message send --channel C123 "hi"` sends "hi"). Anything that reads
 * as a different target is rejected as a conflict rather than guessed at.
 */
export function resolveTargetArgs(input: {
  positionals: (string | undefined)[];
  channel?: string;
}): { target: string; rest: (string | undefined)[] } {
  const [first, ...rest] = input.positionals;
  const channel = input.channel?.trim() || undefined;
  if (!channel) {
    if (!first?.trim()) {
      throw new Error(
        "Missing <target>: pass a Slack message URL, #channel, or channel ID (or --channel <channel>)",
      );
    }
    return { target: first, rest };
  }
  if (first === undefined) {
    return { target: channel, rest };
  }
  if (sameTarget(first, channel)) {
    return { target: first, rest };
  }
  const hasFreeSlot = rest.length > 0 && rest.at(-1) === undefined;
  if (!hasFreeSlot || looksLikeExplicitTarget(first)) {
    throw new Error(
      `Conflicting targets: positional "${first}" and --channel "${channel}". Pass only one.`,
    );
  }
  return { target: channel, rest: [first, ...rest.slice(0, -1)] };
}
