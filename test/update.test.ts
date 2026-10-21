import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { chmod, copyFile, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  compareSemver,
  detectInstallMethod,
  removeStaleBinaryBackup,
  replaceBinary,
} from "../src/lib/update.ts";

describe("compareSemver", () => {
  test("equal versions return 0", () => {
    expect(compareSemver("1.2.3", "1.2.3")).toBe(0);
  });

  test("strips v prefix", () => {
    expect(compareSemver("v1.2.3", "1.2.3")).toBe(0);
  });

  test("newer major", () => {
    expect(compareSemver("2.0.0", "1.9.9")).toBeGreaterThan(0);
  });

  test("newer minor", () => {
    expect(compareSemver("1.3.0", "1.2.9")).toBeGreaterThan(0);
  });

  test("newer patch", () => {
    expect(compareSemver("1.2.4", "1.2.3")).toBeGreaterThan(0);
  });

  test("older version returns negative", () => {
    expect(compareSemver("0.1.0", "0.2.0")).toBeLessThan(0);
  });

  test("handles 0.x versions", () => {
    expect(compareSemver("0.2.10", "0.2.9")).toBeGreaterThan(0);
    expect(compareSemver("0.2.10", "0.2.10")).toBe(0);
    expect(compareSemver("0.2.10", "0.3.0")).toBeLessThan(0);
  });
});

describe("replaceBinary", () => {
  let dir: string;
  let currentBinary: string;
  let newBinary: string;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), "agent-slack-update-test-"));
    currentBinary = join(dir, "agent-slack.exe");
    newBinary = join(dir, "download.exe");
    await writeFile(currentBinary, "old");
    await writeFile(newBinary, "new");
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  const eperm = () => Object.assign(new Error("EPERM: operation not permitted"), { code: "EPERM" });

  test("removes the backup once the new binary is in place", async () => {
    await replaceBinary({ newBinary, currentBinary });

    expect(await readFile(currentBinary, "utf8")).toBe("new");
    expect(await Bun.file(`${currentBinary}.bak`).exists()).toBe(false);
  });

  test("keeps the update when Windows refuses to delete the running binary's backup", async () => {
    // Windows: the old image is still running, so deleting it fails.
    const fs = {
      rename,
      copyFile,
      chmod,
      rm: async () => {
        throw eperm();
      },
    };

    await replaceBinary({ newBinary, currentBinary }, fs);

    expect(await readFile(currentBinary, "utf8")).toBe("new");
    expect(await readFile(`${currentBinary}.bak`, "utf8")).toBe("old");
  });

  test("restores the old binary when copying the new one fails", async () => {
    const fs = {
      rename,
      chmod,
      rm,
      copyFile: async () => {
        throw new Error("disk full");
      },
    };

    await expect(replaceBinary({ newBinary, currentBinary }, fs)).rejects.toThrow("disk full");
    expect(await readFile(currentBinary, "utf8")).toBe("old");
  });

  test("clears a backup left by an earlier update before replacing", async () => {
    await writeFile(`${currentBinary}.bak`, "stale");

    await replaceBinary({ newBinary, currentBinary });

    expect(await readFile(currentBinary, "utf8")).toBe("new");
    expect(await Bun.file(`${currentBinary}.bak`).exists()).toBe(false);
  });

  test("removeStaleBinaryBackup deletes a leftover backup and never throws", async () => {
    await writeFile(`${currentBinary}.bak`, "stale");
    await removeStaleBinaryBackup(currentBinary);
    expect(await Bun.file(`${currentBinary}.bak`).exists()).toBe(false);

    await removeStaleBinaryBackup(currentBinary, {
      rename,
      copyFile,
      chmod,
      rm: async () => {
        throw eperm();
      },
    });
  });
});

describe("detectInstallMethod", () => {
  test.each([
    { execPath: "/usr/local/bin/bun", isBun: true, expected: "bun" },
    { execPath: "C:\\Users\\me\\.bun\\bin\\bun.exe", isBun: true, expected: "bun" },
    { execPath: "C:\\Program Files\\nodejs\\NODE.EXE", isBun: false, expected: "npm" },
    { execPath: "/usr/bin/node", isBun: false, expected: "npm" },
    { execPath: "/opt/agent-slack/agent-slack", isBun: true, expected: "binary" },
    { execPath: "C:\\tools\\agent-slack.exe", isBun: true, expected: "binary" },
    {
      execPath: "/opt/agent-slack/agent-slack",
      isBun: false,
      npmExecpath: "/usr/lib/npm-cli.js",
      expected: "npm",
    },
  ])("$execPath (bun=$isBun) is $expected", ({ expected, ...runtime }) => {
    expect(detectInstallMethod(runtime)).toBe(expected);
  });
});
