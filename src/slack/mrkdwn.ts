import { slackEmojiShortcodesToUnicode } from "./emoji.ts";

export function slackMrkdwnToMarkdown(text: string): string {
  if (!text) {
    return "";
  }

  // Links: <http://x|label> or <http://x>
  let out = text.replace(/<((https?:\/\/)[^>|]+)\|([^>]+)>/g, "[$3]($1)");
  out = out.replace(/<((https?:\/\/)[^>]+)>/g, "$1");

  // Channels: <#C123|name>, or the bare id for <#C123> / <#C123|>
  out = out.replace(/<#[A-Z0-9]+\|([^>]+)>/g, "#$1");
  out = out.replace(/<#([A-Z0-9]+)\|?>/g, "#$1");

  // Users: <@U123> or <@U123|name>
  out = out.replace(/<@([A-Z0-9]+)\|([^>]+)>/g, "@$2");
  out = out.replace(/<@([A-Z0-9]+)>/g, "@$1");

  // User groups: <!subteam^S123|@handle>, or the bare id for <!subteam^S123>
  out = out.replace(/<!subteam\^[A-Z0-9]+\|@?([^>]+)>/g, "@$1");
  out = out.replace(/<!subteam\^([A-Z0-9]+)\|?>/g, "@$1");

  // Dates: <!date^1700000000^{date_short}^optional_link|fallback>, else format the timestamp
  out = out.replace(/<!date\^\d+\^[^|>]*\|([^>]+)>/g, "$1");
  out = out.replace(
    /<!date\^(\d+)\^[^|>]*\|?>/g,
    (match, ts: string) => formatUnixSecondsUtc(Number(ts)) ?? match,
  );

  // Special mentions: <!here>, <!channel>, <!everyone>
  out = out.replace(/<!([a-zA-Z]+)>/g, "@$1");

  // Decode basic HTML entities Slack sometimes includes
  out = out.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

  // Prefer unicode emoji for token efficiency (e.g. 🚀 vs :rocket:)
  out = slackEmojiShortcodesToUnicode(out);

  return out;
}

/** e.g. "2023-11-14 22:13 UTC"; undefined when the timestamp is outside the Date range. */
function formatUnixSecondsUtc(sec: number): string | undefined {
  const date = new Date(sec * 1000);
  if (!Number.isFinite(date.getTime())) {
    return undefined;
  }
  return `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}
