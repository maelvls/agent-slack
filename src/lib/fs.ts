import {
  link,
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  rm,
  utimes,
  writeFile,
} from "node:fs/promises";
import { randomBytes } from "node:crypto";
import { dirname } from "node:path";
import { isRecord } from "./object-type-guards.ts";

export async function readJsonFile<T>(path: string): Promise<T | null> {
  try {
    const raw = await readFile(path, "utf8");
    return JSON.parse(raw) as T;
  } catch (err: unknown) {
    if (isRecord(err) && err.code === "ENOENT") {
      return null;
    }
    // Corrupted JSON should not brick the CLI; treat as missing.
    if (err instanceof SyntaxError) {
      return null;
    }
    throw err;
  }
}

/**
 * Write via a temp file + rename so a concurrent reader (or a crash mid-write)
 * never sees a truncated or interleaved file.
 */
export async function writeFileAtomic(path: string, contents: string): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  const tmp = `${path}.${process.pid}.${randomBytes(4).toString("hex")}.tmp`;
  try {
    await writeFile(tmp, contents, { mode: 0o600 });
    await rename(tmp, path);
  } catch (err) {
    await rm(tmp, { force: true }).catch(() => {});
    throw err;
  }
}

export async function writeJsonFile(path: string, data: unknown): Promise<void> {
  await writeFileAtomic(path, `${JSON.stringify(data, null, 2)}\n`);
}

/** Like writeJsonFile, without indentation, for large cache files. */
export async function writeCompactJsonFile(path: string, data: unknown): Promise<void> {
  await writeFileAtomic(path, `${JSON.stringify(data)}\n`);
}

/**
 * Run `fn` while holding an O_EXCL lockfile. Best effort: after `waitMs` (or if
 * the lock can't be created at all) `fn` runs anyway rather than bricking the CLI.
 *
 * The lockfile holds `<pid> <token>`. A lock whose process is gone, or that hasn't
 * been refreshed for `staleMs`, is taken over. The holder refreshes its mtime while
 * `fn` runs, but keychain calls block the event loop for seconds at a time, so
 * `staleMs` stays well above that. Release only removes a lock this call still owns.
 */
export async function withFileLock<T>(
  options: { lockPath: string; staleMs?: number; waitMs?: number },
  fn: () => Promise<T>,
): Promise<T> {
  const { lockPath } = options;
  const staleMs = options.staleMs ?? 120_000;
  const deadline = Date.now() + (options.waitMs ?? 20_000);
  const owner = `${process.pid} ${randomBytes(16).toString("hex")}`;
  let locked = false;
  try {
    await mkdir(dirname(lockPath), { recursive: true });
  } catch {
    // open() below fails too and we proceed unlocked
  }
  for (;;) {
    try {
      const handle = await open(lockPath, "wx", 0o600);
      try {
        await handle.writeFile(owner);
      } finally {
        await handle.close();
      }
      locked = true;
      break;
    } catch (err: unknown) {
      if (!(isRecord(err) && err.code === "EEXIST")) {
        break;
      }
    }
    if (await takeOverIfStale(lockPath, staleMs)) {
      continue;
    }
    if (Date.now() >= deadline) {
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 20 + Math.random() * 30));
  }
  const heartbeat = locked
    ? setInterval(
        () => {
          void refreshLock(lockPath, owner);
        },
        Math.max(staleMs / 3, 10),
      )
    : undefined;
  heartbeat?.unref();
  try {
    return await fn();
  } finally {
    clearInterval(heartbeat);
    if (locked) {
      await releaseLock(lockPath, owner);
    }
  }
}

async function readLockOwner(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

function lockHolderIsGone(owner: string | null): boolean {
  const pid = Number(owner?.split(" ")[0]);
  if (!Number.isInteger(pid) || pid <= 0 || pid === process.pid) {
    return false;
  }
  try {
    process.kill(pid, 0);
    return false;
  } catch (err: unknown) {
    return isRecord(err) && err.code === "ESRCH";
  }
}

/**
 * Remove a stale lock so the caller can retry creating it. Returns false when the
 * lock is live or isn't a regular file we can safely remove; the caller then waits
 * out its deadline instead of spinning. The stale lock is renamed aside first so
 * only one of several waiters takes it, and a fresh lock that replaced it in the
 * meantime is put back.
 */
async function takeOverIfStale(lockPath: string, staleMs: number): Promise<boolean> {
  let info;
  try {
    info = await lstat(lockPath);
  } catch (err: unknown) {
    // Released between open() and lstat(): retry right away.
    return isRecord(err) && err.code === "ENOENT";
  }
  if (!info.isFile()) {
    return false;
  }
  const owner = await readLockOwner(lockPath);
  if (Date.now() - info.mtimeMs < staleMs && !lockHolderIsGone(owner)) {
    return false;
  }
  const aside = `${lockPath}.${process.pid}.${randomBytes(4).toString("hex")}.stale`;
  try {
    await rename(lockPath, aside);
  } catch {
    // Another waiter took it first; retry creating the lock.
    return true;
  }
  if ((await readLockOwner(aside)) !== owner) {
    // A new holder replaced the stale lock before our rename; put theirs back.
    await link(aside, lockPath).catch(() => {});
  }
  await rm(aside, { force: true }).catch(() => {});
  return true;
}

async function refreshLock(lockPath: string, owner: string): Promise<void> {
  if ((await readLockOwner(lockPath)) === owner) {
    const now = new Date();
    await utimes(lockPath, now, now).catch(() => {});
  }
}

async function releaseLock(lockPath: string, owner: string): Promise<void> {
  if ((await readLockOwner(lockPath)) === owner) {
    await rm(lockPath, { force: true }).catch(() => {});
  }
}
