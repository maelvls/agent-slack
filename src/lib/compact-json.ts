export type PruneEmptyOptions = {
  // Top-level array keys emitted as [] instead of being dropped when empty, so
  // that `jq '.messages[]'` on "no results" yields nothing rather than an error.
  // Keys that are absent (or null) on the input stay absent.
  keepEmptyArrays?: readonly string[];
};

export function pruneEmpty<T>(value: T, options?: PruneEmptyOptions): T {
  const keep = options?.keepEmptyArrays ?? [];
  if (keep.length > 0 && isPlainObject(value)) {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) {
      const next = pruneEmptyInternal(v);
      if (next !== undefined) {
        out[k] = next;
      } else if (Array.isArray(v) && keep.includes(k)) {
        out[k] = [];
      }
    }
    return out as T;
  }

  const pruned = pruneEmptyInternal(value);
  return (pruned === undefined ? ({} as T) : (pruned as T)) as T;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function pruneEmptyInternal(value: unknown): unknown {
  if (value === null || value === undefined) {
    return undefined;
  }

  if (typeof value === "string") {
    return value.trim() === "" ? undefined : value;
  }

  if (typeof value === "number" || typeof value === "boolean") {
    return value;
  }

  if (Array.isArray(value)) {
    const next = value
      .map((v) => pruneEmptyInternal(v))
      .filter((v): v is Exclude<unknown, undefined> => v !== undefined);
    return next.length === 0 ? undefined : next;
  }

  if (typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const next = pruneEmptyInternal(v);
      if (next !== undefined) {
        out[k] = next;
      }
    }
    return Object.keys(out).length === 0 ? undefined : out;
  }

  return value;
}
