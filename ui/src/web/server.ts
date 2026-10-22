// HTTP side of the web management panel. It serves the same renderer bundle as
// the desktop app, behind a login with the management key, and forwards the
// panel's calls to the handler table the Electron IPC uses (main/handlers.ts).
//
//   GET  /                 the panel (login page until a session exists)
//   GET  /api/session      whether this browser is logged in
//   POST /api/login        { key } -> session cookie
//   POST /api/logout
//   POST /api/invoke/<k>   { args } -> { result, effects }; <k> is a key of CHANNELS
//   GET  /api/events       server-sent events: snapshot, calls, ask, logout
//   POST /api/answer       { id, value } for an ask
//
// Every POST needs `X-Xmesh-Request: 1`, a JSON body, and (when the browser sends
// one) a same-origin Origin, so another site cannot drive the panel through the
// session cookie.

import { randomBytes } from "node:crypto";
import * as fs from "node:fs";
import type { IncomingMessage, ServerResponse } from "node:http";
import { CHANNELS } from "../shared/ipc";
import type { CallEventData } from "../shared/control";
import type { Snapshot } from "../shared/state";
import { WEB_EVENTS, WEB_PATHS, WEB_REQUEST_HEADER, WEB_SESSION_COOKIE, parseWebAnswer, type WebAsk, type WebInvokeReply, type WebSessionInfo } from "../shared/web";
import type { Handler } from "../main/handlers";
import { PANEL_CSP, mimeFor, resolvePanelFile } from "../main/policy";
import { keyMatches, LoginLimiter, Sessions } from "./auth";
import type { WebBridge } from "./bridge";

/** The renderer bundle talks to its own origin; nothing else changes from the desktop CSP. */
export const WEB_CSP = PANEL_CSP.replace("connect-src 'none'", "connect-src 'self'");

const MAX_BODY = 4 * 1024 * 1024;

export interface SnapshotSource {
  readonly snapshot: Snapshot;
  on(event: "change", listener: (s: Snapshot) => void): unknown;
  on(event: "calls", listener: (c: CallEventData[]) => void): unknown;
  off(event: "change", listener: (s: Snapshot) => void): unknown;
  off(event: "calls", listener: (c: CallEventData[]) => void): unknown;
}

export interface WebServerOptions {
  /** The management key; read on every login, so a rotation applies at once. */
  key: () => string;
  handlers: Map<string, Handler>;
  controller: SnapshotSource;
  /** Built with `send` from this server's `sendAsk`. */
  bridge: WebBridge;
  /** dist/web: index.html, web.js and the renderer bundle. */
  publicRoot: string;
  /** Served over TLS: the cookie gets `Secure`. */
  secure: boolean;
  sessions?: Sessions;
  limiter?: LoginLimiter;
  /** SSE keep-alive; also how often an ended session is noticed on an open stream. */
  pingMs?: number;
  log?: (line: string) => void;
}

interface Stream {
  session: string;
  res: ServerResponse;
}

const CHANNEL_BY_KEY = new Map<string, string>(Object.entries(CHANNELS));

class HttpError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

function securityHeaders(type: string): Record<string, string> {
  return {
    "Content-Type": type,
    "Content-Security-Policy": WEB_CSP,
    "X-Content-Type-Options": "nosniff",
    "X-Frame-Options": "DENY",
    "Referrer-Policy": "no-referrer",
    "Cross-Origin-Opener-Policy": "same-origin",
    "Cross-Origin-Resource-Policy": "same-origin",
    "Cache-Control": "no-store",
  };
}

export function parseCookies(header: string | undefined): Map<string, string> {
  const out = new Map<string, string>();
  for (const part of (header ?? "").split(";")) {
    const i = part.indexOf("=");
    if (i <= 0) continue;
    out.set(part.slice(0, i).trim(), part.slice(i + 1).trim());
  }
  return out;
}

/** Whether a state-changing request comes from the panel's own page. */
export function sameOrigin(req: IncomingMessage, secure: boolean): boolean {
  const site = req.headers["sec-fetch-site"];
  if (site !== undefined && site !== "same-origin") return false;
  const origin = req.headers.origin;
  if (origin === undefined) return true;
  const host = req.headers.host;
  return host !== undefined && origin === `${secure ? "https" : "http"}://${host}`;
}

function readBody(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on("data", (c: Buffer) => {
      size += c.length;
      if (size > MAX_BODY) {
        reject(new HttpError(413, "request too large"));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on("end", () => {
      try {
        resolve(size === 0 ? {} : JSON.parse(Buffer.concat(chunks).toString("utf8")));
      } catch {
        reject(new HttpError(400, "invalid JSON"));
      }
    });
    req.on("error", reject);
  });
}

function sendJson(res: ServerResponse, status: number, body: unknown, extra: Record<string, string | string[]> = {}): void {
  const text = JSON.stringify(body);
  res.writeHead(status, { ...securityHeaders("application/json; charset=utf-8"), ...extra });
  res.end(text);
}

/** JSON for SSE: one `data:` line, no raw newlines. */
function sse(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

export class WebServer {
  private readonly sessions: Sessions;
  private readonly limiter: LoginLimiter;
  private readonly streams = new Set<Stream>();
  private readonly log: (line: string) => void;
  private snapshotTimer: NodeJS.Timeout | undefined;
  private callsTimer: NodeJS.Timeout | undefined;
  private pendingCalls: CallEventData[] | undefined;
  private readonly ping: NodeJS.Timeout;

  constructor(private readonly opts: WebServerOptions) {
    this.sessions = opts.sessions ?? new Sessions();
    this.limiter = opts.limiter ?? new LoginLimiter();
    this.log = opts.log ?? (() => {});
    opts.controller.on("change", this.onChange);
    opts.controller.on("calls", this.onCalls);
    this.ping = setInterval(() => this.keepAlive(), opts.pingMs ?? 15_000);
    this.ping.unref();
  }

  /** Delivers an ask to every open stream of `session`. */
  readonly sendAsk = (session: string, ask: WebAsk): boolean => {
    let sent = false;
    for (const s of this.streams) {
      if (s.session !== session) continue;
      s.res.write(sse(WEB_EVENTS.ask, ask));
      sent = true;
    }
    return sent;
  };

  readonly handle = (req: IncomingMessage, res: ServerResponse): void => {
    this.route(req, res).catch((e: unknown) => {
      const status = e instanceof HttpError ? e.status : 500;
      if (status === 500) this.log(`web: ${req.method} ${req.url}: ${e instanceof Error ? e.message : String(e)}`);
      if (!res.headersSent) sendJson(res, status, { error: e instanceof HttpError ? e.message : "internal error" });
      else res.end();
    });
  };

  /** Ends every session and stream (the key was rotated, or the server stops). */
  logoutAll(): void {
    this.sessions.clear();
    for (const s of this.streams) {
      s.res.end(sse(WEB_EVENTS.logout, {}));
      this.opts.bridge.cancel(s.session);
    }
    this.streams.clear();
  }

  close(): void {
    clearInterval(this.ping);
    clearTimeout(this.snapshotTimer);
    clearTimeout(this.callsTimer);
    this.opts.controller.off("change", this.onChange);
    this.opts.controller.off("calls", this.onCalls);
    this.logoutAll();
  }

  private readonly onChange = (): void => {
    if (this.snapshotTimer) return;
    this.snapshotTimer = setTimeout(() => {
      this.snapshotTimer = undefined;
      this.broadcast(WEB_EVENTS.snapshot, this.opts.controller.snapshot);
    }, 50);
  };

  // Bursts of calls are coalesced so a busy kernel cannot flood the page (as in ipc.ts).
  private readonly onCalls = (calls: CallEventData[]): void => {
    this.pendingCalls = calls;
    if (this.callsTimer) return;
    this.callsTimer = setTimeout(() => {
      this.callsTimer = undefined;
      if (this.pendingCalls) this.broadcast(WEB_EVENTS.calls, this.pendingCalls);
      this.pendingCalls = undefined;
    }, 250);
  };

  private broadcast(event: string, data: unknown): void {
    if (this.streams.size === 0) return;
    const text = sse(event, data);
    for (const s of this.streams) s.res.write(text);
  }

  private keepAlive(): void {
    for (const s of this.streams) {
      if (this.sessions.touch(s.session)) s.res.write(": ping\n\n");
      else this.dropStream(s, true);
    }
  }

  private dropStream(s: Stream, notify: boolean): void {
    if (!this.streams.delete(s)) return;
    if (notify) s.res.end(sse(WEB_EVENTS.logout, {}));
    if (![...this.streams].some((o) => o.session === s.session)) this.opts.bridge.cancel(s.session);
  }

  private session(req: IncomingMessage): string | undefined {
    const id = parseCookies(req.headers.cookie).get(WEB_SESSION_COOKIE);
    return id !== undefined && this.sessions.touch(id) ? id : undefined;
  }

  private cookie(value: string, maxAge: number): string {
    return `${WEB_SESSION_COOKIE}=${value}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${maxAge}${this.opts.secure ? "; Secure" : ""}`;
  }

  private async route(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://localhost");
    const p = url.pathname;
    if (!p.startsWith("/api/")) {
      if (req.method !== "GET" && req.method !== "HEAD") throw new HttpError(405, "method not allowed");
      return this.serveFile(req, res, p);
    }

    if (p === WEB_PATHS.session && req.method === "GET") {
      const wait = this.limiter.wait(req.socket.remoteAddress ?? "");
      const info: WebSessionInfo = { authenticated: this.session(req) !== undefined, ...(wait > 0 && { retryAfter: Math.ceil(wait / 1000) }) };
      return sendJson(res, 200, info);
    }

    if (p === WEB_PATHS.events && req.method === "GET") {
      const session = this.session(req);
      if (!session) throw new HttpError(401, "login required");
      return this.openStream(req, res, session);
    }

    if (req.method !== "POST") throw new HttpError(405, "method not allowed");
    if (req.headers[WEB_REQUEST_HEADER] !== "1" || !sameOrigin(req, this.opts.secure)) throw new HttpError(403, "cross-origin request refused");
    if (!/^application\/json\b/.test(req.headers["content-type"] ?? "")) throw new HttpError(415, "JSON required");

    if (p === WEB_PATHS.login) return this.login(req, res);
    if (p === WEB_PATHS.logout) {
      const id = parseCookies(req.headers.cookie).get(WEB_SESSION_COOKIE);
      this.sessions.delete(id);
      for (const s of [...this.streams]) if (s.session === id) this.dropStream(s, true);
      return sendJson(res, 200, { ok: true }, { "Set-Cookie": this.cookie("", 0) });
    }

    const session = this.session(req);
    if (!session) throw new HttpError(401, "login required");
    const body = await readBody(req);

    if (p === WEB_PATHS.answer) {
      const a = parseWebAnswer(body);
      if (!a || !this.opts.bridge.answer(session, a)) throw new HttpError(400, "unknown or invalid answer");
      return sendJson(res, 200, { ok: true });
    }

    if (p.startsWith(WEB_PATHS.invoke)) {
      const channel = CHANNEL_BY_KEY.get(p.slice(WEB_PATHS.invoke.length));
      const fn = channel === undefined ? undefined : this.opts.handlers.get(channel);
      if (!fn) throw new HttpError(404, "unknown operation");
      const args = (body as { args?: unknown }).args ?? [];
      if (!Array.isArray(args) || args.length > 4) throw new HttpError(400, "invalid arguments");
      const { value, effects } = await this.opts.bridge.run(session, () => fn(...args));
      const reply: WebInvokeReply = { result: value === undefined ? null : value, effects };
      return sendJson(res, 200, reply);
    }

    throw new HttpError(404, "not found");
  }

  private async login(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const client = req.socket.remoteAddress ?? "";
    const wait = this.limiter.wait(client);
    if (wait > 0) {
      const retryAfter = Math.ceil(wait / 1000);
      return sendJson(res, 429, { error: "too many attempts", retryAfter }, { "Retry-After": String(retryAfter) });
    }
    const body = await readBody(req);
    if (!keyMatches(this.opts.key(), (body as { key?: unknown }).key)) {
      this.limiter.fail(client);
      this.log(`web: failed login from ${client}`);
      return sendJson(res, 401, { error: "invalid key" });
    }
    this.limiter.succeed(client);
    const old = parseCookies(req.headers.cookie).get(WEB_SESSION_COOKIE);
    this.sessions.delete(old);
    const id = this.sessions.create();
    this.log(`web: login from ${client}`);
    return sendJson(res, 200, { ok: true }, { "Set-Cookie": this.cookie(id, 7 * 24 * 3600) });
  }

  private openStream(req: IncomingMessage, res: ServerResponse, session: string): void {
    res.writeHead(200, {
      ...securityHeaders("text/event-stream; charset=utf-8"),
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    });
    res.write("retry: 2000\n\n");
    res.write(sse(WEB_EVENTS.snapshot, this.opts.controller.snapshot));
    const stream: Stream = { session, res };
    this.streams.add(stream);
    req.on("close", () => this.dropStream(stream, false));
  }

  private async serveFile(req: IncomingMessage, res: ServerResponse, pathname: string): Promise<void> {
    const file = resolvePanelFile(this.opts.publicRoot, `app://panel${pathname}`);
    const type = file === undefined ? undefined : mimeFor(file);
    if (file === undefined || type === undefined) throw new HttpError(404, "not found");
    let body: Buffer;
    try {
      body = await fs.promises.readFile(file);
    } catch {
      throw new HttpError(404, "not found");
    }
    const headers = securityHeaders(type);
    if (type.startsWith("text/html")) {
      const nonce = randomBytes(24).toString("base64");
      body = Buffer.from(body.toString("utf8").replaceAll("__XMESH_STYLE_NONCE__", nonce));
      headers["Content-Security-Policy"] = headers["Content-Security-Policy"]!.replaceAll("__XMESH_STYLE_NONCE__", nonce);
    }
    res.writeHead(200, headers);
    res.end(req.method === "HEAD" ? undefined : body);
  }
}
