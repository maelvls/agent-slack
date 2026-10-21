import { afterEach, beforeEach, describe, expect, mock, spyOn, test } from "bun:test";
import { request as httpRequest } from "node:http";
import { browserOpenCommand, openDraftEditor } from "../src/cli/draft-server.ts";
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

async function startEditor(
  onSend = mock(async (_text: string) => ({ ts: "1.000001" })),
  browserOpens: boolean | "hang" = true,
) {
  let resolveUrl: (url: string) => void = () => {};
  const urlPromise = new Promise<string>((r) => {
    resolveUrl = r;
  });
  const result = openDraftEditor({
    channelName: "general",
    channelId: "C12345678",
    initialText: "hello",
    onSend,
    openerTimeoutMs: 20,
    openBrowser: async (url) => {
      resolveUrl(url);
      if (browserOpens === "hang") {
        await new Promise(() => {});
      }
      if (!browserOpens) {
        throw new Error("no browser");
      }
    },
  });
  const url = new URL(await urlPromise);
  const port = Number(url.port);
  const token = new URLSearchParams(url.hash.slice(1)).get("token") ?? "";
  return { port, token, result, onSend };
}

function stderrOutput(): string {
  return (process.stderr.write as unknown as ReturnType<typeof mock>).mock.calls
    .map((call) => String(call[0]))
    .join("");
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

  test("serves the editor only to a loopback Host, without the session token", async () => {
    const { port, token, result } = await startEditor();
    expect(token).toHaveLength(64);

    const page = await send({ port, method: "GET", path: "/" });
    expect(page.status).toBe(200);
    expect(page.body).toContain("hello");
    expect(page.body).not.toContain(token);

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

  test("keeps the token off stderr when the browser opens", async () => {
    const { port, token, result } = await startEditor();
    await new Promise((r) => setTimeout(r, 0));

    expect(stderrOutput()).toContain(`http://127.0.0.1:${port}/`);
    expect(stderrOutput()).not.toContain(token);
    await cancel(port, token);
    await result;
  });

  test("prints the URL with the token when the browser cannot be opened", async () => {
    const { port, token, result } = await startEditor(undefined, false);
    await new Promise((r) => setTimeout(r, 0));

    expect(stderrOutput()).toContain(`http://127.0.0.1:${port}/#token=${token}`);
    await cancel(port, token);
    await result;
  });

  test("prints the URL with the token when the opener doesn't report back", async () => {
    const { port, token, result } = await startEditor(undefined, "hang");
    await new Promise((r) => setTimeout(r, 0));
    expect(stderrOutput()).toBe("");

    await new Promise((r) => setTimeout(r, 60));
    expect(stderrOutput()).toContain(`http://127.0.0.1:${port}/#token=${token}`);
    await cancel(port, token);
    await result;
  });

  test("serves a page that explains a missing session instead of failing on send", async () => {
    const { port, token, result } = await startEditor();
    const page = await send({ port, method: "GET", path: "/" });
    expect(page.body).toContain("This tab has no editor session");
    expect(page.body).toContain("if (!resp.ok)");
    await cancel(port, token);
    await result;
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

describe("browserOpenCommand", () => {
  const url = "http://127.0.0.1:1234/#token=abc&x=^1";

  test("uses open on macOS", () => {
    expect(browserOpenCommand(url, { platform: "darwin", env: {} })).toEqual({
      cmd: "open",
      args: [url],
    });
  });

  test("uses the URL protocol handler on Windows, passing the URL verbatim", () => {
    expect(browserOpenCommand(url, { platform: "win32", env: {} })).toEqual({
      cmd: "rundll32",
      args: ["url.dll,FileProtocolHandler", url],
    });
  });

  test("uses xdg-open on Linux only when there is a display", () => {
    expect(browserOpenCommand(url, { platform: "linux", env: {} })).toBeNull();
    expect(browserOpenCommand(url, { platform: "linux", env: { DISPLAY: ":0" } })).toEqual({
      cmd: "xdg-open",
      args: [url],
    });
    expect(
      browserOpenCommand(url, { platform: "linux", env: { WAYLAND_DISPLAY: "wayland-0" } }),
    ).not.toBeNull();
  });
});
