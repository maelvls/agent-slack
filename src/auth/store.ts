import { CREDENTIALS_FILE, KEYCHAIN_SERVICE } from "./paths.ts";
import { readFile, writeFile } from "node:fs/promises";
import { withFileLock, writeJsonFile } from "../lib/fs.ts";
import { isRecord } from "../lib/object-type-guards.ts";
import { WorkspaceSchema, type Credentials, type Workspace } from "./schema.ts";
import { keychainGet, keychainSet } from "./keychain.ts";
import { platform } from "node:os";

const KEYCHAIN_PLACEHOLDER = "__KEYCHAIN__";
// The secret lives in a per-workspace keychain account rather than the shared one.
const KEYCHAIN_WORKSPACE_PLACEHOLDER = "__KEYCHAIN_WORKSPACE__";
const IS_MACOS = platform() === "darwin";

function normalizeWorkspaceUrl(workspaceUrl: string): string {
  const u = new URL(workspaceUrl);
  return `${u.protocol}//${u.host}`;
}

function isPlaceholderSecret(value: string | undefined): boolean {
  return !value || value === KEYCHAIN_PLACEHOLDER || value === KEYCHAIN_WORKSPACE_PLACEHOLDER;
}

// The shared "xoxd" keychain entry holds the cookie most browser workspaces use
// (one keychain item). A workspace whose cookie differs, e.g. imported from
// another browser, gets its own `xoxd:<url>` entry, or stays in the file if the
// keychain write fails.
export function xoxdCookieForFile(input: {
  cookie: string;
  sharedXoxd: string | undefined;
  storeOwn: () => boolean;
}): string {
  const { cookie, sharedXoxd } = input;
  if (isPlaceholderSecret(cookie)) {
    return cookie;
  }
  if (sharedXoxd !== undefined && cookie === sharedXoxd) {
    return KEYCHAIN_PLACEHOLDER;
  }
  return input.storeOwn() ? KEYCHAIN_WORKSPACE_PLACEHOLDER : cookie;
}

export function hydrateXoxdCookie(
  fileCookie: string,
  keychain: { shared: () => string | null; own: () => string | null },
): string {
  if (fileCookie === KEYCHAIN_WORKSPACE_PLACEHOLDER) {
    return keychain.own() ?? fileCookie;
  }
  if (isPlaceholderSecret(fileCookie)) {
    return keychain.shared() ?? fileCookie;
  }
  return fileCookie;
}

const EMPTY_CREDENTIALS: Credentials = { version: 1, workspaces: [] };

/**
 * Validate workspaces one at a time so a single bad entry doesn't drop the rest.
 * `invalid` is set when anything had to be discarded.
 */
export function parseCredentials(raw: unknown): { credentials: Credentials; invalid: boolean } {
  if (raw === null || raw === undefined) {
    return { credentials: { ...EMPTY_CREDENTIALS, workspaces: [] }, invalid: false };
  }
  if (!isRecord(raw) || raw.version !== 1) {
    return { credentials: { ...EMPTY_CREDENTIALS, workspaces: [] }, invalid: true };
  }
  let invalid = false;
  const rawWorkspaces = raw.workspaces ?? [];
  if (!Array.isArray(rawWorkspaces)) {
    invalid = true;
  }
  const workspaces: Workspace[] = [];
  for (const w of Array.isArray(rawWorkspaces) ? rawWorkspaces : []) {
    const parsed = WorkspaceSchema.safeParse(w);
    if (parsed.success) {
      workspaces.push(parsed.data);
    } else {
      invalid = true;
    }
  }
  const credentials: Credentials = { version: 1, workspaces };
  if (typeof raw.updated_at === "string") {
    credentials.updated_at = raw.updated_at;
  }
  if (typeof raw.default_workspace_url === "string" && URL.canParse(raw.default_workspace_url)) {
    credentials.default_workspace_url = raw.default_workspace_url;
  } else if (raw.default_workspace_url !== undefined) {
    invalid = true;
  }
  return { credentials, invalid };
}

/**
 * Read and validate a credentials file without touching the keychain. An
 * unparseable or partly invalid file is copied to `<path>.invalid` before the
 * next save can overwrite it, and a warning goes to stderr.
 */
export async function readCredentialsFile(path: string): Promise<Credentials> {
  let text: string;
  try {
    text = await readFile(path, "utf8");
  } catch (err: unknown) {
    if (isRecord(err) && err.code === "ENOENT") {
      return { ...EMPTY_CREDENTIALS, workspaces: [] };
    }
    throw err;
  }

  let result: { credentials: Credentials; invalid: boolean };
  try {
    result = parseCredentials(JSON.parse(text) as unknown);
  } catch {
    result = { credentials: { ...EMPTY_CREDENTIALS, workspaces: [] }, invalid: true };
  }
  if (result.invalid) {
    const backupPath = `${path}.invalid`;
    let existing: string | null = null;
    try {
      existing = await readFile(backupPath, "utf8");
    } catch {
      // no backup yet
    }
    if (existing !== text) {
      await writeFile(backupPath, text, { mode: 0o600 });
    }
    console.error(
      `agent-slack: ${path} is invalid; kept ${result.credentials.workspaces.length} valid workspace(s). ` +
        `The original was saved to ${backupPath}.`,
    );
  }
  return result.credentials;
}

export async function loadCredentials(): Promise<Credentials> {
  const creds = await readCredentialsFile(CREDENTIALS_FILE);

  // Optional: hydrate browser cookie/token from keychain for security.
  const hydrated = creds.workspaces.map((w) => {
    if (w.auth.auth_type === "browser") {
      const url = normalizeWorkspaceUrl(w.workspace_url);
      const xoxc = keychainGet(`xoxc:${url}`, KEYCHAIN_SERVICE);
      return {
        ...w,
        auth: {
          auth_type: "browser" as const,
          xoxc_token: xoxc ?? w.auth.xoxc_token,
          xoxd_cookie: hydrateXoxdCookie(w.auth.xoxd_cookie, {
            shared: () => keychainGet("xoxd", KEYCHAIN_SERVICE),
            own: () => keychainGet(`xoxd:${url}`, KEYCHAIN_SERVICE),
          }),
        },
      };
    }
    if (w.auth.auth_type === "standard") {
      const account = `token:${normalizeWorkspaceUrl(w.workspace_url)}`;
      const token = keychainGet(account, KEYCHAIN_SERVICE);
      return {
        ...w,
        auth: {
          auth_type: "standard" as const,
          token: token ?? w.auth.token,
        },
      };
    }
    return w;
  });

  return { ...creds, workspaces: hydrated };
}

function storeInKeychain(account: string, value: string): boolean {
  return (
    keychainGet(account, KEYCHAIN_SERVICE) === value ||
    keychainSet({ account, value, service: KEYCHAIN_SERVICE })
  );
}

export async function saveCredentials(credentials: Credentials): Promise<void> {
  const payload: Credentials = {
    ...credentials,
    updated_at: new Date().toISOString(),
    workspaces: credentials.workspaces.map((w) => ({
      ...w,
      workspace_url: normalizeWorkspaceUrl(w.workspace_url),
    })),
  };

  // Store secrets in keychain when possible and avoid writing plaintext tokens to disk.
  // If keychain writes fail (or non-macOS), fall back to storing secrets in the file.
  const filePayload: Credentials = structuredClone(payload);

  if (IS_MACOS) {
    // Browser auth: xoxd is shared across workspaces, xoxc is per-workspace.
    const firstBrowser = payload.workspaces.find((w) => w.auth.auth_type === "browser");
    let sharedXoxd: string | undefined;
    if (
      firstBrowser?.auth.auth_type === "browser" &&
      !isPlaceholderSecret(firstBrowser.auth.xoxd_cookie)
    ) {
      const cookie = firstBrowser.auth.xoxd_cookie;
      sharedXoxd = storeInKeychain("xoxd", cookie) ? cookie : undefined;
    }

    for (const w of filePayload.workspaces) {
      if (w.auth.auth_type === "browser") {
        const url = normalizeWorkspaceUrl(w.workspace_url);
        const { auth } = w;
        const tokenStored =
          isPlaceholderSecret(auth.xoxc_token) || storeInKeychain(`xoxc:${url}`, auth.xoxc_token);

        if (tokenStored) {
          auth.xoxc_token = KEYCHAIN_PLACEHOLDER;
        }
        auth.xoxd_cookie = xoxdCookieForFile({
          cookie: auth.xoxd_cookie,
          sharedXoxd,
          storeOwn: () => storeInKeychain(`xoxd:${url}`, auth.xoxd_cookie),
        });
      }

      if (w.auth.auth_type === "standard") {
        const account = `token:${normalizeWorkspaceUrl(w.workspace_url)}`;
        const tokenStored =
          isPlaceholderSecret(w.auth.token) || storeInKeychain(account, w.auth.token);
        if (tokenStored) {
          w.auth.token = KEYCHAIN_PLACEHOLDER;
        }
      }
    }
  }

  await writeJsonFile(CREDENTIALS_FILE, filePayload);
}

/** Serialize load-modify-save so concurrent agent-slack processes don't drop each other's writes. */
function withCredentialsLock<T>(fn: () => Promise<T>): Promise<T> {
  return withFileLock({ lockPath: `${CREDENTIALS_FILE}.lock` }, fn);
}

export function upsertWorkspace(workspace: Workspace): Promise<Workspace> {
  return withCredentialsLock(async () => {
    const creds = await loadCredentials();
    const normalizedUrl = normalizeWorkspaceUrl(workspace.workspace_url);
    const next: Workspace = { ...workspace, workspace_url: normalizedUrl };

    const idx = creds.workspaces.findIndex(
      (w) => normalizeWorkspaceUrl(w.workspace_url) === normalizedUrl,
    );
    if (idx === -1) {
      creds.workspaces.push(next);
    } else {
      creds.workspaces[idx] = {
        ...creds.workspaces[idx],
        ...next,
        auth: next.auth,
      };
    }

    if (!creds.default_workspace_url) {
      creds.default_workspace_url = normalizedUrl;
    }
    await saveCredentials(creds);
    return next;
  });
}

export async function upsertWorkspaces(workspaces: Workspace[]): Promise<void> {
  if (workspaces.length === 0) {
    return;
  }
  await withCredentialsLock(async () => {
    const creds = await loadCredentials();

    for (const workspace of workspaces) {
      const normalizedUrl = normalizeWorkspaceUrl(workspace.workspace_url);
      const next: Workspace = { ...workspace, workspace_url: normalizedUrl };

      const idx = creds.workspaces.findIndex(
        (w) => normalizeWorkspaceUrl(w.workspace_url) === normalizedUrl,
      );
      if (idx === -1) {
        creds.workspaces.push(next);
      } else {
        creds.workspaces[idx] = {
          ...creds.workspaces[idx],
          ...next,
          auth: next.auth,
        };
      }

      if (!creds.default_workspace_url) {
        creds.default_workspace_url = normalizedUrl;
      }
    }

    await saveCredentials(creds);
  });
}

export function setDefaultWorkspace(workspaceUrl: string): Promise<void> {
  return withCredentialsLock(async () => {
    const creds = await loadCredentials();
    creds.default_workspace_url = normalizeWorkspaceUrl(workspaceUrl);
    await saveCredentials(creds);
  });
}

export function removeWorkspace(workspaceUrl: string): Promise<void> {
  return withCredentialsLock(async () => {
    const creds = await loadCredentials();
    const normalized = normalizeWorkspaceUrl(workspaceUrl);
    creds.workspaces = creds.workspaces.filter(
      (w) => normalizeWorkspaceUrl(w.workspace_url) !== normalized,
    );
    if (creds.default_workspace_url === normalized) {
      creds.default_workspace_url = creds.workspaces[0]?.workspace_url;
    }
    await saveCredentials(creds);
  });
}

export async function resolveWorkspaceForUrl(workspaceUrl: string): Promise<Workspace | null> {
  const creds = await loadCredentials();
  const normalized = normalizeWorkspaceUrl(workspaceUrl);
  return (
    creds.workspaces.find((w) => normalizeWorkspaceUrl(w.workspace_url) === normalized) ?? null
  );
}

export async function resolveDefaultWorkspace(): Promise<Workspace | null> {
  const creds = await loadCredentials();
  if (creds.default_workspace_url) {
    const byDefault = creds.workspaces.find((w) => w.workspace_url === creds.default_workspace_url);
    if (byDefault) {
      return byDefault;
    }
  }
  return creds.workspaces[0] ?? null;
}

// (reserved for future non-secret per-workspace metadata)
