import { describe, expect, test } from "bun:test";
import { parsePemCertificates } from "../src/lib/keychain-ca.ts";

describe("parsePemCertificates", () => {
  test("returns an empty array for input with no certificates", () => {
    expect(parsePemCertificates("")).toEqual([]);
    expect(parsePemCertificates("not a cert")).toEqual([]);
  });

  test("extracts a single PEM block", () => {
    const pem = "-----BEGIN CERTIFICATE-----\nAAAA\nBBBB\n-----END CERTIFICATE-----";
    expect(parsePemCertificates(pem)).toEqual([pem]);
  });

  test("extracts multiple concatenated PEM blocks, as `security find-certificate -a -p` emits", () => {
    const first = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----";
    const second = "-----BEGIN CERTIFICATE-----\nBBBB\n-----END CERTIFICATE-----";
    expect(parsePemCertificates(`${first}\n${second}\n`)).toEqual([first, second]);
  });

  test("ignores surrounding noise", () => {
    const pem = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----";
    expect(parsePemCertificates(`some preamble\n${pem}\ntrailing text`)).toEqual([pem]);
  });
});
