import { execFileSync } from "node:child_process";
import { rootCertificates } from "node:tls";
import { getKeychainTimeoutMs } from "../auth/keychain.ts";

const SYSTEM_KEYCHAIN = "/Library/Keychains/System.keychain";

export function parsePemCertificates(input: string): string[] {
  return input.match(/-----BEGIN CERTIFICATE-----[\s\S]*?-----END CERTIFICATE-----/g) ?? [];
}

function readSystemKeychainCertificates(): string[] {
  try {
    const output = execFileSync("security", ["find-certificate", "-a", "-p", SYSTEM_KEYCHAIN], {
      encoding: "utf8",
      stdio: ["pipe", "pipe", "ignore"],
      timeout: getKeychainTimeoutMs(),
    });
    return parsePemCertificates(output);
  } catch {
    return [];
  }
}

let cachedCa: string[] | undefined;
let computed = false;

/**
 * Bun's fetch() and https.Agent don't consult the macOS Keychain the way curl/Safari do, so a
 * corporate TLS-intercepting proxy (Zscaler, Palo Alto Networks GlobalProtect, Netskope, etc.)
 * whose root CA is installed system-wide shows up to Bun as a self-signed certificate error.
 * Node has its own `--use-system-ca` flag for this; Bun doesn't, so on macOS + Bun we read the
 * System keychain's certificates ourselves and merge them with Bun's bundled public roots
 * (Bun's `tls.ca` option replaces, rather than extends, the default trust store).
 */
export function getKeychainTlsCa(): string[] | undefined {
  if (!process.versions.bun || process.platform !== "darwin") {
    return undefined;
  }
  if (!computed) {
    computed = true;
    const extra = readSystemKeychainCertificates();
    cachedCa = extra.length > 0 ? [...rootCertificates, ...extra] : undefined;
  }
  return cachedCa;
}

/** Spread into a `fetch()` init or a `WebClient` options object; a no-op outside macOS + Bun. */
export function getKeychainTlsOption(): { tls: { ca: string[] } } | Record<string, never> {
  const ca = getKeychainTlsCa();
  return ca ? { tls: { ca } } : {};
}
