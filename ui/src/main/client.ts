import * as http from "node:http";
import {
  CONTROL_VERSION_HEADER,
  ControlRequestError,
  type ConfigDiagnostic,
  type ControlError,
  type ControlEvent,
  type DrainResponse,
  type Peer,
  type StatusResponse,
  VersionMismatchError,
  isSupportedApiVersion,
  parseEvent,
  parsePeers,
  parseStatus,
} from "../shared/control";
import { SseParser } from "../shared/sse";

export interface ClientOptions {
  /** Unix socket path or Windows named pipe path (`endpoint` of control.json). */
  endpoint: string;
  kernelVersion?: string;
  requestTimeoutMs?: number;
  /**
   * An event stream with no bytes for this long is dropped and reconnected.
   * The kernel pings every 15 s; the contract says to give up after 45 s.
   */
  streamIdleMs?: number;
}

interface Reply {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: unknown;
}

export interface EventStream {
  /** Resolves when the stream ends (kernel closed it, network error, idle timeout, or `close()`); never rejects. */
  done: Promise<void>;
  close(): void;
}

const VERSION_HEADER = CONTROL_VERSION_HEADER.toLowerCase();
const HOST = "xmesh-control";

function headerValue(headers: http.IncomingHttpHeaders, name: string): string {
  const v = headers[name];
  return Array.isArray(v) ? (v[0] ?? "") : (v ?? "");
}

/**
 * Control API v1 over a Unix domain socket or named pipe. Node's http module
 * speaks both through `socketPath`; nothing here ever opens a TCP port.
 */
export class ControlClient {
  private readonly endpoint: string;
  private readonly kernelVersion: string;
  private readonly timeoutMs: number;
  private readonly streamIdleMs: number;
  private lastEventId: string | undefined;

  constructor(opts: ClientOptions) {
    this.endpoint = opts.endpoint;
    this.kernelVersion = opts.kernelVersion ?? "";
    this.timeoutMs = opts.requestTimeoutMs ?? 5000;
    this.streamIdleMs = opts.streamIdleMs ?? 45_000;
  }

  async getStatus(): Promise<StatusResponse> {
    return parseStatus((await this.json("GET", "/v1/status")).body);
  }

  async getPeers(): Promise<Peer[]> {
    return parsePeers((await this.json("GET", "/v1/peers")).body);
  }

  /** `keep` refuses new calls and leaves running PTYs and execs alone. */
  async drain(mode: "keep" | "kill" = "keep"): Promise<DrainResponse> {
    return (await this.json("POST", "/v1/drain", { mode })).body as DrainResponse;
  }

  async resume(): Promise<DrainResponse> {
    return (await this.json("POST", "/v1/resume")).body as DrainResponse;
  }

  /**
   * Opens `GET /v1/events` and delivers parsed events (including `hello` and
   * `reset`). Reconnecting is the caller's job: this client remembers the last
   * event id and sends it as `Last-Event-ID` the next time it opens a stream,
   * and the kernel answers with the missed events or a `reset`.
   */
  openEvents(onEvent: (e: ControlEvent) => void): EventStream {
    let req: http.ClientRequest | undefined;
    let closed = false;
    const done = new Promise<void>((resolve) => {
      const headers: Record<string, string> = { Accept: "text/event-stream", Host: HOST };
      if (this.lastEventId !== undefined) headers["Last-Event-ID"] = this.lastEventId;
      req = http.request({ socketPath: this.endpoint, path: "/v1/events", method: "GET", headers }, (res) => {
        if (res.statusCode !== 200 || !isSupportedApiVersion(headerValue(res.headers, VERSION_HEADER))) {
          res.resume();
          resolve();
          return;
        }
        const parser = new SseParser();
        res.setEncoding("utf8");
        res.on("data", (chunk: string) => {
          for (const m of parser.push(chunk)) {
            if (m.id !== undefined) this.lastEventId = m.id;
            const event = parseEvent(m.data);
            if (event) onEvent(event);
          }
        });
        res.on("end", resolve);
        res.on("close", resolve);
        res.on("error", () => resolve());
      });
      req.setTimeout(this.streamIdleMs, () => req?.destroy());
      req.on("error", () => resolve());
      req.end();
    });
    return {
      done,
      close: () => {
        if (closed) return;
        closed = true;
        req?.destroy();
      },
    };
  }

  /**
   * Any other Control API route. The caller names the method and path, so only
   * main-process code with fixed paths may use it (see panel-service.ts).
   */
  async call(method: string, path: string, opts: { body?: unknown; ifMatch?: string } = {}): Promise<unknown> {
    const headers: Record<string, string> = opts.ifMatch === undefined ? {} : { "If-Match": `"${opts.ifMatch}"` };
    return (await this.json(method, path, opts.body, headers)).body;
  }

  private async json(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<Reply> {
    const reply = await this.request(method, path, body, extra);
    const version = headerValue(reply.headers, VERSION_HEADER);
    if (!isSupportedApiVersion(version)) throw new VersionMismatchError(version, this.kernelVersion);
    if (reply.status >= 400) {
      const b = reply.body as Partial<Record<keyof ControlError, unknown>> | null;
      const details: ConstructorParameters<typeof ControlRequestError>[4] = {};
      if (typeof b?.field === "string") details.field = b.field;
      if (typeof b?.current_etag === "string") details.current_etag = b.current_etag;
      if (typeof b?.retry_after_ms === "number") details.retry_after_ms = b.retry_after_ms;
      if (Array.isArray(b?.diagnostics)) {
        details.diagnostics = b.diagnostics.filter(
          (d): d is ConfigDiagnostic => typeof d === "object" && d !== null && typeof (d as ConfigDiagnostic).message === "string",
        );
      }
      throw new ControlRequestError(
        typeof b?.error === "string" ? b.error : "internal",
        typeof b?.message === "string" ? b.message : `HTTP ${reply.status}`,
        reply.status,
        typeof b?.reason === "string" ? b.reason : undefined,
        details,
      );
    }
    return reply;
  }

  private request(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<Reply> {
    return new Promise((resolve, reject) => {
      const payload = body === undefined ? undefined : JSON.stringify(body);
      const headers: Record<string, string | number> = { Accept: "application/json", Host: HOST, ...extra };
      if (payload !== undefined) {
        headers["Content-Type"] = "application/json";
        headers["Content-Length"] = Buffer.byteLength(payload);
      }
      const req = http.request({ socketPath: this.endpoint, path, method, headers }, (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("error", reject);
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          let parsed: unknown = null;
          if (text !== "") {
            try {
              parsed = JSON.parse(text);
            } catch {
              parsed = null;
            }
          }
          resolve({ status: res.statusCode ?? 0, headers: res.headers, body: parsed });
        });
      });
      req.setTimeout(this.timeoutMs, () => req.destroy(new Error("control request timed out")));
      req.on("error", reject);
      if (payload !== undefined) req.write(payload);
      req.end();
    });
  }
}
