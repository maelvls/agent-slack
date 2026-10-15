/**
 * Shared CLI option helpers for repeatable file-attachment flags. Kept here so
 * `message send`, `message draft create`, and `message draft update` share one
 * reducer and one path-normalization routine instead of three private copies.
 * Also hosts the helpers that accept the alternative spellings agents guess.
 */

import { Option, type Command } from "commander";

/** Commander reducer for a repeatable string option (e.g. `--attach`). */
export function collectOptionValue(value: string, previous: string[] = []): string[] {
  return [...previous, value];
}

/** Trim and de-duplicate repeatable `--attach` paths (order preserved). */
export function normalizeAttachPaths(raw: string[] | undefined): string[] {
  if (!Array.isArray(raw) || raw.length === 0) {
    return [];
  }
  const out: string[] = [];
  for (const p of raw.map((v) => String(v).trim()).filter(Boolean)) {
    if (!out.includes(p)) {
      out.push(p);
    }
  }
  return out;
}

/**
 * Register a hidden alternative spelling for an existing option: values given
 * via `flags` are stored under `target` (the primary option's attribute name),
 * so the action only ever reads the primary.
 */
export function addHiddenOptionAlias(
  cmd: Command,
  input: { flags: string; target: string },
): Command {
  const alias = new Option(input.flags).hideHelp();
  cmd.addOption(alias);
  cmd.on(`option:${alias.name()}`, (value: string) => {
    cmd.setOptionValueWithSource(input.target, value, "cli");
  });
  return cmd;
}

/** Pick a value given either positionally or via its flag; both must agree. */
export function pickPositionalOrFlag(input: {
  positional?: string;
  flag?: string;
  positionalName: string;
  flagName: string;
}): string | undefined {
  if (
    input.positional !== undefined &&
    input.flag !== undefined &&
    input.positional !== input.flag
  ) {
    throw new Error(
      `Conflicting values for ${input.positionalName} and ${input.flagName}. Pass only one.`,
    );
  }
  return input.positional ?? input.flag;
}
