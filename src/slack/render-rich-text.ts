/**
 * Rich-text block rendering helpers extracted from render.ts.
 */

import { isRecord } from "../lib/object-type-guards.ts";

const ENCODED_ID_PATTERN = /^[A-Z0-9]+$/;

function getString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function isSafeEncodedId(value: string): boolean {
  return ENCODED_ID_PATTERN.test(value);
}

function nonNegativeInt(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

export function extractMrkdwnFromRichTextBlock(block: unknown): string {
  if (!isRecord(block)) {
    return "";
  }
  const elements = Array.isArray(block.elements) ? block.elements : [];
  let out = "";
  let prevWasList = false;
  for (const el of elements) {
    const txt = extractMrkdwnFromRichTextElement(el);
    if (!txt.trim()) {
      continue;
    }
    // Nested list levels arrive as sibling lists; keep them in one block so the indent nests.
    const isList = isRecord(el) && el.type === "rich_text_list";
    if (out) {
      out += isList && prevWasList ? "\n" : "\n\n";
    }
    out += txt;
    prevWasList = isList;
  }
  return out;
}

function extractMrkdwnFromRichTextElement(el: unknown): string {
  if (!isRecord(el)) {
    return "";
  }
  const t = getString(el.type);

  if (t === "rich_text_section") {
    const parts: string[] = [];
    for (const child of Array.isArray(el.elements) ? el.elements : []) {
      parts.push(extractMrkdwnFromRichTextElement(child));
    }
    return parts.join("");
  }

  if (t === "rich_text_preformatted") {
    const parts: string[] = [];
    for (const child of Array.isArray(el.elements) ? el.elements : []) {
      parts.push(extractMrkdwnFromRichTextElement(child));
    }
    const text = parts.join("");
    return text ? `\`\`\`${text}\`\`\`` : "";
  }

  if (t === "rich_text_quote") {
    const parts: string[] = [];
    for (const child of Array.isArray(el.elements) ? el.elements : []) {
      parts.push(extractMrkdwnFromRichTextElement(child));
    }
    const text = parts.join("").trim();
    if (!text) {
      return "";
    }
    return text
      .split("\n")
      .map((line) => `> ${line}`)
      .join("\n");
  }

  if (t === "rich_text_list") {
    const style = typeof el.style === "string" ? el.style : "bullet";
    const items: string[] = [];
    const itemEls = Array.isArray(el.elements) ? el.elements : [];
    const indent = "  ".repeat(nonNegativeInt(el.indent));
    // An ordered list split by a nested list resumes numbering via `offset`.
    let num = nonNegativeInt(el.offset);
    for (const item of itemEls) {
      const txt = extractMrkdwnFromRichTextElement(item).trim();
      if (!txt) {
        continue;
      }
      num++;
      const prefix = style === "ordered" ? `${num}. ` : "- ";
      items.push(`${indent}${prefix}${txt}`);
    }
    return items.join("\n");
  }

  if (t === "text") {
    const raw = getString(el.text);
    const style = isRecord(el.style) ? el.style : null;
    if (!style) {
      return raw;
    }
    let text = raw;
    if (style.code) {
      text = `\`${text}\``;
    }
    if (style.bold) {
      text = `*${text}*`;
    }
    if (style.italic) {
      text = `_${text}_`;
    }
    if (style.strike) {
      text = `~${text}~`;
    }
    return text;
  }

  if (t === "link") {
    const url = getString(el.url);
    const text = getString(el.text);
    if (!url) {
      return text;
    }
    return text ? `<${url}|${text}>` : url;
  }

  if (t === "emoji") {
    const name = getString(el.name);
    return name ? `:${name}:` : "";
  }

  if (t === "user") {
    const userId = getString(el.user_id);
    return isSafeEncodedId(userId) ? `<@${userId}>` : "";
  }

  if (t === "channel") {
    const channelId = getString(el.channel_id);
    return isSafeEncodedId(channelId) ? `<#${channelId}>` : "";
  }

  if (t === "usergroup") {
    const usergroupId = getString(el.usergroup_id);
    return isSafeEncodedId(usergroupId) ? `<!subteam^${usergroupId}>` : "";
  }

  if (t === "date") {
    // Re-emit as mrkdwn <!date^ts^format|fallback>; delimiters are stripped so a crafted
    // format or fallback can't break out of the token.
    const fallback = getString(el.fallback).replace(/[<>]/g, "");
    const ts = el.timestamp;
    if (typeof ts !== "number" || !Number.isInteger(ts) || ts < 0) {
      return fallback;
    }
    const format = getString(el.format).replace(/[<>|^]/g, "") || "{date_short}";
    return `<!date^${ts}^${format}${fallback ? `|${fallback}` : ""}>`;
  }

  if (t === "broadcast") {
    const range = getString(el.range);
    return range === "here" || range === "channel" || range === "everyone" ? `<!${range}>` : "";
  }

  return "";
}
