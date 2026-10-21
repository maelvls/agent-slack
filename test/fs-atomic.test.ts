import { afterAll, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, readdir, readFile, rm, stat, utimes, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { withFileLock, writeJsonFile } from "../src/lib/fs.ts";

const dirs: string[] = [];
afterAll(async () => {
  await Promise.all(dirs.map((d) => rm(d, { recursive: true, force: true })));
});

async function tempDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "agent-slack-fs-"));
  dirs.push(dir);
  return dir;
}

describe("writeJsonFile", () => {
  test("concurrent writes of different sizes always leave valid JSON and no temp files", async () => {
    const dir = await tempDir();
    const path = join(dir, "data.json");
    const long = { items: Array.from({ length: 2000 }, (_, i) => `item-${i}`) };
    const short = { items: ["x"] };
    await Promise.all(
      Array.from({ length: 20 }, (_, i) => writeJsonFile(path, i % 2 ? long : short)),
    );
    const text = await readFile(path, "utf8");
    expect(() => JSON.parse(text)).not.toThrow();
    expect(await readdir(dir)).toEqual(["data.json"]);
  });

  test("replaces an existing file with mode 0600", async () => {
    const dir = await tempDir();
    const path = join(dir, "data.json");
    await writeFile(path, "old", { mode: 0o644 });
    await writeJsonFile(path, { a: 1 });
    expect(JSON.parse(await readFile(path, "utf8"))).toEqual({ a: 1 });
    expect((await stat(path)).mode & 0o777).toBe(0o600);
  });
});

describe("withFileLock", () => {
  test("serializes read-modify-write so no increment is lost", async () => {
    const dir = await tempDir();
    const path = join(dir, "counter.json");
    const lock = join(dir, "counter.lock");
    await writeJsonFile(path, { n: 0 });
    await Promise.all(
      Array.from({ length: 10 }, () =>
        withFileLock({ lockPath: lock }, async () => {
          const { n } = JSON.parse(await readFile(path, "utf8")) as { n: number };
          await new Promise((r) => setTimeout(r, 5));
          await writeJsonFile(path, { n: n + 1 });
        }),
      ),
    );
    expect(JSON.parse(await readFile(path, "utf8"))).toEqual({ n: 10 });
    expect(await readdir(dir)).not.toContain("counter.lock");
  });

  test("takes over a stale lock", async () => {
    const dir = await tempDir();
    const lock = join(dir, "stale.lock");
    await writeFile(lock, "");
    const result = await withFileLock(
      { lockPath: lock, staleMs: 0, waitMs: 1000 },
      async () => "ran",
    );
    expect(result).toBe("ran");
  });

  test("runs anyway after waiting for a held lock", async () => {
    const dir = await tempDir();
    const lock = join(dir, "held.lock");
    await writeFile(lock, "");
    const result = await withFileLock(
      { lockPath: lock, staleMs: 60_000, waitMs: 50 },
      async () => "ran",
    );
    expect(result).toBe("ran");
  });

  test("gives up on a lock path that is a directory instead of spinning", async () => {
    const dir = await tempDir();
    const lock = join(dir, "dir.lock");
    await mkdir(lock);
    await utimes(lock, new Date(0), new Date(0));
    const started = Date.now();
    const result = await withFileLock(
      { lockPath: lock, staleMs: 0, waitMs: 100 },
      async () => "ran",
    );
    expect(result).toBe("ran");
    expect(Date.now() - started).toBeLessThan(2000);
    expect((await stat(lock)).isDirectory()).toBe(true);
  });

  test("takes over a lock whose process is gone", async () => {
    const dir = await tempDir();
    const lock = join(dir, "dead.lock");
    // PIDs this large are never assigned, so the holder counts as gone.
    await writeFile(lock, "2147483646 deadbeef");
    const result = await withFileLock(
      { lockPath: lock, staleMs: 60_000, waitMs: 50 },
      async () => (await readFile(lock, "utf8")).split(" ")[0],
    );
    expect(result).toBe(String(process.pid));
  });

  test("release leaves a lock that another holder took over", async () => {
    const dir = await tempDir();
    const lock = join(dir, "owned.lock");
    await withFileLock({ lockPath: lock }, async () => {
      await writeFile(lock, "1 someone-else");
    });
    expect(await readFile(lock, "utf8")).toBe("1 someone-else");
  });

  test("refreshes the lock mtime while fn runs", async () => {
    const dir = await tempDir();
    const lock = join(dir, "refresh.lock");
    await withFileLock({ lockPath: lock, staleMs: 60 }, async () => {
      await utimes(lock, new Date(0), new Date(0));
      await new Promise((r) => setTimeout(r, 80));
      expect(Date.now() - (await stat(lock)).mtimeMs).toBeLessThan(60);
    });
  });
});
