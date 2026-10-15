const TRUNCATED_URL_HINT =
  'URL may have been truncated by shell. Quote URLs containing "&":\n' +
  '  agent-slack message get "https://...?thread_ts=...&cid=..."';

// For JSON read commands: returned in the payload (as `warning`) instead of
// written to stderr, so `2>&1 | jq` keeps working.
export function truncatedSlackUrlWarning(ref: { possiblyTruncated?: boolean }): string | undefined {
  return ref.possiblyTruncated ? TRUNCATED_URL_HINT : undefined;
}

export function warnOnTruncatedSlackUrl(ref: { possiblyTruncated?: boolean }): void {
  if (ref.possiblyTruncated) {
    console.error(`Hint: ${TRUNCATED_URL_HINT}`);
  }
}
