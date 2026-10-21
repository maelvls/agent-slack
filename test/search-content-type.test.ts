import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type ContentType,
  searchMessagesInChannelsFallback,
} from "../src/slack/search-messages.ts";
import { searchSlack } from "../src/slack/search.ts";

const AUTH = { auth_type: "standard" as const, token: "xoxb-test" };

// One message per content type; every file has a download URL so a download is
// attempted whenever the message is kept and --no-download isn't set.
const MESSAGES: Record<string, unknown>[] = [
  { ts: "1770000500.000000", text: "needle plain" },
  {
    ts: "1770000400.000000",
    text: "needle image",
    files: [
      {
        id: "FIMG",
        mimetype: "IMAGE/PNG",
        url_private_download: "https://files.slack.com/FIMG.png",
      },
    ],
  },
  {
    ts: "1770000300.000000",
    text: "needle snippet",
    files: [
      {
        id: "FSNIP",
        mimetype: "text/plain",
        mode: "snippet",
        url_private_download: "https://files.slack.com/FSNIP.txt",
      },
    ],
  },
  {
    ts: "1770000200.000000",
    text: "needle pdf",
    files: [
      {
        id: "FPDF",
        mimetype: "application/pdf",
        url_private_download: "https://files.slack.com/FPDF.pdf",
      },
    ],
  },
];

const EXPECTED: Record<Exclude<ContentType, "any">, string[]> = {
  text: ["1770000500.000000"],
  image: ["1770000400.000000"],
  snippet: ["1770000300.000000"],
  file: ["1770000400.000000", "1770000300.000000", "1770000200.000000"],
};

const FILE_IDS_BY_TS: Record<string, string> = {
  "1770000400.000000": "FIMG",
  "1770000300.000000": "FSNIP",
  "1770000200.000000": "FPDF",
};

function fakeClient() {
  return {
    api: async (method: string, params: Record<string, unknown>) => {
      if (method === "conversations.history") {
        // Channel scan: no `latest`. Single-message fetch: `latest` is the ts.
        const latest = params.latest as string | undefined;
        const messages = latest ? MESSAGES.filter((m) => m.ts === latest) : MESSAGES;
        return { ok: true, messages, response_metadata: { next_cursor: "" } };
      }
      if (method === "search.messages") {
        return {
          messages: {
            matches: MESSAGES.map((m) => ({ ts: m.ts, channel: { id: "C12345678" } })),
          },
        };
      }
      if (method === "files.info") {
        return { ok: true };
      }
      throw new Error(`Unexpected API method: ${method}`);
    },
  } as never;
}

let appDir: string;
let downloadedUrls: string[];
const originalXdg = process.env.XDG_RUNTIME_DIR;
const originalFetch = globalThis.fetch;

beforeEach(async () => {
  appDir = await mkdtemp(join(tmpdir(), "agent-slack-search-ct-"));
  process.env.XDG_RUNTIME_DIR = appDir;
  downloadedUrls = [];
  globalThis.fetch = (async (input: string | URL | Request) => {
    downloadedUrls.push(String(input instanceof Request ? input.url : input));
    return new Response("data", { status: 200, headers: { "content-type": "image/png" } });
  }) as typeof fetch;
});

afterEach(async () => {
  globalThis.fetch = originalFetch;
  if (originalXdg === undefined) {
    delete process.env.XDG_RUNTIME_DIR;
  } else {
    process.env.XDG_RUNTIME_DIR = originalXdg;
  }
  await rm(appDir, { recursive: true, force: true });
});

function expectedDownloads(contentType: Exclude<ContentType, "any">): string[] {
  return EXPECTED[contentType]
    .map((ts) => FILE_IDS_BY_TS[ts])
    .filter((id): id is string => Boolean(id))
    .sort();
}

function downloadedIds(): string[] {
  return downloadedUrls.map((u) => u.split("/").pop()!.split(".")[0]!).sort();
}

const CASES = Object.keys(EXPECTED) as Exclude<ContentType, "any">[];

describe("--content-type in the channel history scan", () => {
  for (const contentType of CASES) {
    for (const download of [false, true]) {
      test(`${contentType} ${download ? "with download" : "with --no-download"}`, async () => {
        const result = await searchMessagesInChannelsFallback(fakeClient(), {
          auth: AUTH,
          query: "needle",
          channels: ["C12345678"],
          limit: 10,
          maxContentChars: 4000,
          contentType,
          download,
        });

        expect(result.messages.map((m) => m.ts)).toEqual(EXPECTED[contentType]);
        // Only kept messages get their files downloaded.
        expect(downloadedIds()).toEqual(download ? expectedDownloads(contentType) : []);
      });
    }
  }
});

describe("--content-type via search.messages", () => {
  for (const contentType of CASES) {
    for (const download of [false, true]) {
      test(`${contentType} ${download ? "with download" : "with --no-download"}`, async () => {
        const result = await searchSlack({
          client: fakeClient(),
          auth: AUTH,
          options: {
            query: "needle",
            kind: "messages",
            content_type: contentType,
            limit: 10,
            download,
          },
        });

        expect(result.messages?.map((m) => m.ts)).toEqual(EXPECTED[contentType]);
        expect(downloadedIds()).toEqual(download ? expectedDownloads(contentType) : []);
      });
    }
  }
});
