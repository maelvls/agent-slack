import { afterEach, beforeEach, describe, expect, mock, spyOn, test } from "bun:test";
import { request as httpRequest } from "node:http";
import { openDraftEditor } from "../src/cli/draft-server.ts";
import { startCommandWatchdog, stopCommandWatchdog } from "../src/lib/command-watchdog.ts";

type HttpResponse = { status: number; body: string };

function send(input: {
  port: number;
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: string;
}): Promise<HttpResponse> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(
      {
        host: "127.0.0.1",
        port: input.port,
        method: input.method,
        path: input.path,
        headers: { Host: `127.0.0.1:${input.port}`, ...input.headers },
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () =>
          resolve({ status: res.statusCode ?? 0, body: Buffer.concat(chunks).toString() }),
        );
      },
    );
    req.on("error", reject);
    req.end(input.body);
  });
}

async function startEditor(onSend = mock(async (_text: string) => ({ ts: "1.000001" }))) {
  let resolveUrl: (url: string) => void = () => {};
  const urlPromise = new Promise<string>((r) => {
    resolveUrl = r;
  });
  const result = openDraftEditor({
    channelName: "general",
    channelId: "C12345678",
    initialText: "hello",
    onSend,
    openBrowser: resolveUrl,
  });
  const port = Number(new URL(await urlPromise).port);
  const page = await send({ port, method: "GET", path: "/" });
  const token = /"token":"([0-9a-f]+)"/.exec(page.body)?.[1] ?? "";
  return { port, token, result, onSend };
}

async function cancel(port: number, token: string): Promise<void> {
  await send({
    port,
    method: "POST",
    path: "/cancel",
    headers: { "Content-Type": "application/json", "X-Draft-Token": token },
    body: "{}",
  });
}

describe("openDraftEditor", () => {
  beforeEach(() => {
    spyOn(process.stderr, "write").mockImplementation(() => true);
  });

  afterEach(() => {
    mock.restore();
    stopCommandWatchdog();
  });

  test("disarms the command watchdog while the human edits", async () => {
    const exit = spyOn(process, "exit").mockImplementation((() => {}) as never);
    startCommandWatchdog(20);

    const { port, token, result } = await startEditor();
    await new Promise((r) => setTimeout(r, 60));

    expect(exit).not.toHaveBeenCalled();
    await cancel(port, token);
    expect(await result).toEqual({ cancelled: true });
  });

  test("serves the editor with a session token only to a loopback Host", async () => {
    const { port, token, result } = await startEditor();
    expect(token).toHaveLength(64);

    const rebound = await send({
      port,
      method: "GET",
      path: "/",
      headers: { Host: `attacker.example:${port}` },
    });
    expect(rebound.status).toBe(403);
    expect(rebound.body).not.toContain("hello");
    expect(rebound.body).not.toContain(token);

    const viaLocalhost = await send({
      port,
      method: "GET",
      path: "/",
      headers: { Host: `localhost:${port}` },
    });
    expect(viaLocalhost.status).toBe(200);

    await cancel(port, token);
    await result;
  });

  test.each([
    ["no token", (_token: string) => ({ "Content-Type": "application/json" })],
    [
      "a wrong token",
      (_token: string) => ({ "Content-Type": "application/json", "X-Draft-Token": "0".repeat(64) }),
    ],
    [
      "a form content type",
      (token: string) => ({ "Content-Type": "text/plain", "X-Draft-Token": token }),
    ],
    [
      "a foreign Origin",
      (token: string) => ({
        "Content-Type": "application/json",
        "X-Draft-Token": token,
        Origin: "https://evil.example",
      }),
    ],
  ])("rejects POST /send with %s", async (_label, headersFor) => {
    const { port, token, result, onSend } = await startEditor();

    const resp = await send({
      port,
      method: "POST",
      path: "/send",
      headers: headersFor(token),
      body: JSON.stringify({ text: "pwned" }),
    });

    expect(resp.status).toBe(403);
    expect(onSend).not.toHaveBeenCalled();
    await cancel(port, token);
    expect(await result).toEqual({ cancelled: true });
  });

  test("rejects POST /cancel without the token", async () => {
    const { port, token, result } = await startEditor();

    const resp = await send({
      port,
      method: "POST",
      path: "/cancel",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    expect(resp.status).toBe(403);

    const sent = await send({
      port,
      method: "POST",
      path: "/send",
      headers: { "Content-Type": "application/json", "X-Draft-Token": token },
      body: JSON.stringify({ text: "still open" }),
    });
    expect(sent.status).toBe(200);
    expect(await result).toEqual({ sent: true, text: "still open" });
  });

  test("sends when the page's token, JSON body and loopback Origin are present", async () => {
    const { port, token, result, onSend } = await startEditor();

    const resp = await send({
      port,
      method: "POST",
      path: "/send",
      headers: {
        "Content-Type": "application/json; charset=utf-8",
        "X-Draft-Token": token,
        Origin: `http://127.0.0.1:${port}`,
      },
      body: JSON.stringify({ text: "hi team" }),
    });

    expect(resp.status).toBe(200);
    expect(JSON.parse(resp.body)).toEqual({ ok: true, ts: "1.000001" });
    expect(onSend).toHaveBeenCalledWith("hi team");
    expect(await result).toEqual({ sent: true, text: "hi team" });
  });
});
