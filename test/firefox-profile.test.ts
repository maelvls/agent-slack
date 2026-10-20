import { afterEach, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { listProfileCandidates } from "../src/auth/firefox-profile.ts";

const tempDirs: string[] = [];

afterEach(async () => {
  await Promise.all(tempDirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("listProfileCandidates", () => {
  test("scans the profile directory under the Firefox base dir, not the cwd", async () => {
    const baseDir = await mkdtemp(join(tmpdir(), "agent-slack-firefox-"));
    tempDirs.push(baseDir);
    // macOS keeps profiles in <base>/Profiles; Linux keeps them directly in <base>.
    const profilesDir = process.platform === "darwin" ? join(baseDir, "Profiles") : baseDir;
    const profilePath = join(profilesDir, "abc123.default-release");
    await mkdir(profilePath, { recursive: true });

    expect(await listProfileCandidates(baseDir)).toEqual([{ path: profilePath, isDefault: false }]);
  });
});
