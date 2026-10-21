import { createServer, type IncomingMessage, type Server } from "node:http";
import { spawn } from "node:child_process";
import { randomBytes, timingSafeEqual } from "node:crypto";
import { stopCommandWatchdog } from "../lib/command-watchdog.ts";
// @ts-ignore — import attributes need module:"esnext" in tsconfig; Bun handles this fine
import _editorHtmlContent from "./draft-editor.html" with { type: "text" };

export type DraftEditorConfig = {
  channelName: string;
  channelId?: string;
  workspaceUrl?: string;
  threadTs?: string;
  initialText?: string;
  onSend: (mrkdwn: string) => Promise<{ ts: string }>;
  /** Opens `url` (which carries the session token) in a browser; rejects on failure. */
  openBrowser?: (url: string) => Promise<void>;
  /** How long to wait for the opener before printing the full URL anyway (default 3s). */
  openerTimeoutMs?: number;
};

// An opener that hasn't reported back by then may be blocked (e.g. xdg-open running a
// terminal browser on a headless box), so the human gets the full URL instead.
const DEFAULT_OPENER_TIMEOUT_MS = 3000;

export type DraftResult = { sent: true; text: string } | { cancelled: true };

export function openDraftEditor(config: DraftEditorConfig): Promise<DraftResult> {
  // A human is editing now; the editor has its own idle timeout below.
  stopCommandWatchdog();
  // Required on every POST so that only the page we served can send or cancel:
  // a custom header forces a CORS preflight, which this server never approves.
  // It travels only in the URL fragment given to the browser opener, never in
  // the served HTML or on stderr, so an agent that reads stderr and fetches
  // the page still cannot send without the human.
  const token = randomBytes(32).toString("hex");

  return new Promise<DraftResult>((resolve, reject) => {
    let settled = false;
    let port = 0;
    let openerTimer: ReturnType<typeof setTimeout> | undefined;

    const server: Server = createServer(async (req, res) => {
      // Rejects DNS-rebinding requests, which reach us with a foreign Host.
      if (!isLocalRequest(req, port)) {
        res.writeHead(403);
        res.end("Forbidden");
        return;
      }

      if (req.method === "POST" && !isAuthorizedPost(req, token)) {
        res.writeHead(403, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "forbidden" }));
        return;
      }

      if (req.method === "GET" && (req.url === "/" || req.url === "/index.html")) {
        const html = buildEditorHtml(config);
        res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
        res.end(html);
        return;
      }

      if (req.method === "POST" && req.url === "/send") {
        try {
          const body = await readBody(req);
          const data = JSON.parse(body) as { text: string };
          if (typeof data.text !== "string" || !data.text.trim()) {
            res.writeHead(400, { "Content-Type": "application/json" });
            res.end(JSON.stringify({ ok: false, error: "text is required" }));
            return;
          }
          const sendResult = await config.onSend(data.text);
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ ok: true, ts: sendResult.ts }));
          settled = true;
          resolve({ sent: true, text: data.text });
          setTimeout(() => server.close(), 300);
        } catch (err: unknown) {
          const safeMessage =
            err instanceof Error
              ? err.message.replace(/xox[a-z]-[A-Za-z0-9-]+/g, "[REDACTED]")
              : "Send failed";
          res.writeHead(500, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ ok: false, error: safeMessage }));
        }
        return;
      }

      if (req.method === "POST" && req.url === "/cancel") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
        settled = true;
        resolve({ cancelled: true });
        setTimeout(() => server.close(), 300);
        return;
      }

      res.writeHead(404);
      res.end("Not found");
    });

    server.on("error", (err) => {
      if (!settled) {
        reject(err);
      }
    });

    server.on("close", () => {
      clearTimeout(idleTimeout);
      clearTimeout(openerTimer);
      if (!settled) {
        settled = true;
        resolve({ cancelled: true });
      }
    });

    const idleTimeout = setTimeout(
      () => {
        if (!settled) {
          server.close();
        }
      },
      30 * 60 * 1000,
    );

    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      port = typeof addr === "object" && addr ? addr.port : 0;
      const url = `http://127.0.0.1:${port}/`;
      const urlWithToken = `${url}#token=${token}`;
      // Only the first outcome (opened, failed, or still pending) prints.
      let reported = false;
      const report = (message: string): void => {
        if (!reported) {
          reported = true;
          clearTimeout(openerTimer);
          process.stderr.write(message);
        }
      };
      // The human needs the token to use the editor, so these print it in full.
      openerTimer = setTimeout(
        () => report(`If the draft editor didn't open in your browser, open: ${urlWithToken}\n`),
        config.openerTimeoutMs ?? DEFAULT_OPENER_TIMEOUT_MS,
      );
      openerTimer.unref?.();
      Promise.resolve()
        .then(() => (config.openBrowser ?? openBrowser)(urlWithToken))
        .then(
          () =>
            report(
              `Draft editor opened in your browser (${url} is for reference only; it can't send).\n`,
            ),
          () => report(`Could not open a browser. Open the draft editor at: ${urlWithToken}\n`),
        );
    });
  });
}

const MAX_BODY_BYTES = 1024 * 1024; // 1 MB

function isLocalRequest(req: IncomingMessage, port: number): boolean {
  const allowed = [`127.0.0.1:${port}`, `localhost:${port}`];
  if (!allowed.includes(req.headers.host ?? "")) {
    return false;
  }
  const { origin } = req.headers;
  return origin === undefined || allowed.some((host) => origin === `http://${host}`);
}

function isAuthorizedPost(req: IncomingMessage, token: string): boolean {
  const contentType = (req.headers["content-type"] ?? "").split(";")[0]?.trim().toLowerCase();
  if (contentType !== "application/json") {
    return false;
  }
  const provided = req.headers["x-draft-token"];
  if (typeof provided !== "string" || provided.length !== token.length) {
    return false;
  }
  return timingSafeEqual(Buffer.from(provided), Buffer.from(token));
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let totalBytes = 0;
    req.on("data", (chunk: Buffer) => {
      totalBytes += chunk.length;
      if (totalBytes > MAX_BODY_BYTES) {
        req.destroy();
        reject(new Error("Request body too large"));
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString()));
    req.on("error", reject);
  });
}

/**
 * The command that opens `url` in a browser, or null when there is no display to
 * open it on (Linux without X11/Wayland, e.g. over SSH).
 */
export function browserOpenCommand(
  url: string,
  { platform, env }: { platform: NodeJS.Platform; env: NodeJS.ProcessEnv } = {
    platform: process.platform,
    env: process.env,
  },
): { cmd: string; args: string[] } | null {
  if (platform === "darwin") {
    return { cmd: "open", args: [url] };
  }
  if (platform === "win32") {
    // `start` is a cmd built-in, not an executable, and cmd would also interpret
    // metacharacters in the URL; the URL protocol handler takes it verbatim.
    return { cmd: "rundll32", args: ["url.dll,FileProtocolHandler", url] };
  }
  if (!env.DISPLAY && !env.WAYLAND_DISPLAY) {
    return null;
  }
  return { cmd: "xdg-open", args: [url] };
}

function openBrowser(url: string): Promise<void> {
  const command = browserOpenCommand(url);
  if (!command) {
    return Promise.reject(new Error("No display to open a browser on"));
  }
  return new Promise((resolve, reject) => {
    // Detached with no stdio, so a terminal browser started by the opener can't
    // take over this process's terminal.
    const child = spawn(command.cmd, command.args, { detached: true, stdio: "ignore" });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command.cmd} exited with ${code}`)),
    );
    child.unref();
  });
}

function buildSlackThreadUrl(config: DraftEditorConfig): string | null {
  if (!config.workspaceUrl || !config.channelId || !config.threadTs) {
    return null;
  }
  const tsNoDot = config.threadTs.replaceAll(".", "");
  return `${config.workspaceUrl.replace(/\/$/, "")}/archives/${config.channelId}/p${tsNoDot}`;
}

function extractWorkspaceName(url?: string): string | null {
  if (!url) {
    return null;
  }
  try {
    const host = new URL(url).hostname; // e.g. "stablygroup.slack.com"
    const parts = host.split(".");
    if (parts.length >= 3 && parts.at(-2) === "slack") {
      return parts.slice(0, -2).join("."); // e.g. "stablygroup"
    }
    return host;
  } catch {
    return null;
  }
}

function buildEditorHtml(config: DraftEditorConfig): string {
  const threadUrl = buildSlackThreadUrl(config);
  const workspaceName = extractWorkspaceName(config.workspaceUrl);
  const injectedConfig = JSON.stringify({
    channelName: config.channelName,
    channelId: config.channelId || null,
    workspaceUrl: config.workspaceUrl || null,
    workspaceName,
    threadTs: config.threadTs || null,
    threadUrl,
    initialText: config.initialText || "",
  });

  // JSON.stringify handles quotes/backslashes; escape < and > to prevent
  // </script> breakout and other HTML injection in <script> context.
  const safeConfig = injectedConfig.replace(/</g, "\\u003c").replace(/>/g, "\\u003e");
  return getEditorHtml().replace("__DRAFT_CONFIG__", safeConfig);
}

function getEditorHtml(): string {
  return _editorHtmlContent;
}
