import type { SlackApiClient, SlackAuth } from "./client.ts";
import { ensureDownloadsDir } from "../lib/tmp-paths.ts";
import { resolveChannelId } from "./channels.ts";
import { tryDownloadSlackFile } from "./files.ts";
import { inferExt } from "./search-file-ext.ts";
import { dateToUnixSeconds, resolveUserId } from "./search-query.ts";
import { asArray, getString, isRecord } from "../lib/object-type-guards.ts";

export type ContentType = "any" | "text" | "image" | "snippet" | "file";

export type SearchFile = { title?: string; mimetype?: string; mode?: string; path: string };
export type FileDownloadError = { file_id: string; error: string };
export type SearchFilesResult = { files: SearchFile[]; download_errors: FileDownloadError[] };

export async function searchFilesViaSearchApi(
  client: SlackApiClient,
  input: {
    auth: SlackAuth;
    slack_query: string;
    limit: number;
    contentType: ContentType;
    rawMatches: Record<string, unknown>[];
  },
): Promise<SearchFilesResult> {
  const matches = input.rawMatches;
  if (matches.length === 0) {
    return { files: [], download_errors: [] };
  }

  const downloadsDir = await ensureDownloadsDir();
  const out: SearchFile[] = [];
  const downloadErrors: FileDownloadError[] = [];

  for (const f of matches) {
    const mode = getString(f.mode);
    const mimetype = getString(f.mimetype);
    if (!passesFileContentTypeFilter({ mode, mimetype }, input.contentType)) {
      continue;
    }
    const url = getString(f.url_private_download) ?? getString(f.url_private);
    if (!url) {
      continue;
    }
    const ext = inferExt({
      mimetype,
      filetype: getString(f.filetype),
      name: getString(f.name),
      title: getString(f.title),
    });
    const id = getString(f.id);
    if (!id) {
      continue;
    }
    const result = await tryDownloadSlackFile({
      auth: input.auth,
      url,
      destDir: downloadsDir,
      preferredName: `${id}${ext ? `.${ext}` : ""}`,
    });
    if (!result.ok) {
      downloadErrors.push({ file_id: id, error: result.error });
      continue;
    }
    const title = (getString(f.title) || getString(f.name) || "").trim();
    out.push({
      title: title || undefined,
      mimetype,
      mode,
      path: result.path,
    });
    if (out.length >= input.limit) {
      break;
    }
  }

  return { files: out, download_errors: downloadErrors };
}

export async function searchFilesInChannelsFallback(
  client: SlackApiClient,
  input: {
    auth: SlackAuth;
    query: string;
    channels: string[];
    user?: string;
    after?: string;
    before?: string;
    limit: number;
    contentType: ContentType;
  },
): Promise<SearchFilesResult> {
  const channelIds = await Promise.all(input.channels.map((c) => resolveChannelId(client, c)));
  const userId = input.user ? await resolveUserId(client, input.user) : undefined;
  const queryLower = input.query.trim().toLowerCase();

  const ts_from = input.after ? dateToUnixSeconds(input.after, "start") : undefined;
  const ts_to = input.before ? dateToUnixSeconds(input.before, "end") : undefined;

  const downloadsDir = await ensureDownloadsDir();
  const out: SearchFile[] = [];
  const downloadErrors: FileDownloadError[] = [];

  for (const channelId of channelIds) {
    let page = 1;
    for (;;) {
      const resp = await client.api("files.list", {
        channel: channelId,
        user: userId,
        ts_from,
        ts_to,
        count: 100,
        page,
      });
      const files = isRecord(resp) ? asArray(resp.files).filter(isRecord) : [];
      if (files.length === 0) {
        break;
      }

      for (const f of files) {
        const mode = getString(f.mode);
        const mimetype = getString(f.mimetype);
        if (!passesFileContentTypeFilter({ mode, mimetype }, input.contentType)) {
          continue;
        }

        const title = (getString(f.title) || getString(f.name) || "").trim();
        if (queryLower && !title.toLowerCase().includes(queryLower)) {
          continue;
        }

        const url = getString(f.url_private_download) ?? getString(f.url_private);
        if (!url) {
          continue;
        }

        const ext = inferExt({
          mimetype,
          filetype: getString(f.filetype),
          name: getString(f.name),
          title: getString(f.title),
        });
        const id = getString(f.id);
        if (!id) {
          continue;
        }
        const result = await tryDownloadSlackFile({
          auth: input.auth,
          url,
          destDir: downloadsDir,
          preferredName: `${id}${ext ? `.${ext}` : ""}`,
        });
        if (!result.ok) {
          downloadErrors.push({ file_id: id, error: result.error });
          continue;
        }
        out.push({
          title: title || undefined,
          mimetype,
          mode,
          path: result.path,
        });
        if (out.length >= input.limit) {
          return { files: out, download_errors: downloadErrors };
        }
      }

      const paging = isRecord(resp)
        ? isRecord(resp.paging)
          ? resp.paging
          : resp.pagination
        : null;
      const pages = Number(isRecord(paging) ? (paging.pages ?? paging.page_count) : undefined);
      if (Number.isFinite(pages) && page >= pages) {
        break;
      }
      page++;
    }
  }

  return { files: out, download_errors: downloadErrors };
}

function passesFileContentTypeFilter(
  f: { mode?: string; mimetype?: string },
  contentType: ContentType,
): boolean {
  if (contentType === "any") {
    return true;
  }
  if (contentType === "file") {
    return true;
  }
  if (contentType === "snippet") {
    return f.mode === "snippet";
  }
  if (contentType === "image") {
    return String(f.mimetype ?? "")
      .toLowerCase()
      .startsWith("image/");
  }
  if (contentType === "text") {
    return String(f.mimetype ?? "") === "text/plain";
  }
  return true;
}
