import * as fs from "node:fs";
import * as http from "node:http";
import * as os from "node:os";
import * as path from "node:path";
import { randomBytes } from "node:crypto";
import {
  CONTROL_API_VERSION,
  CONTROL_VERSION_HEADER,
  type Capability,
  type ControlEndpointFile,
  type ControlEvent,
  type DrainResponse,
  type DrainState,
  type Peer,
  type StatusResponse,
  type Warning,
} from "../src/shared/control";
import { fixture, peersFixture, statusFixture } from "./fixtures";
import { FakePlayground } from "./playground";
import { PanelData } from "./panel-routes";

export interface FakeKernelOptions {
  playground?: boolean;
  /** Defaults to a unique socket (POSIX) or named pipe (Windows). */
  endpoint?: string;
  /** Value of X-Xmesh-Control-Version and of api_version in control.json. Default: the UI's CONTROL_API_VERSION. */
  controlVersion?: string;
  kernelVersion?: string;
  /** Capabilities to advertise. Routes of a capability that is not listed answer 501 not_implemented. */
  capabilities?: Capability[];
  /** Replaces fields of the healthy StatusResponse fixture. */
  status?: Partial<StatusResponse>;
  peers?: Peer[];
  /** Interval of `: ping` comments on event streams. Default 15000. */
  pingMs?: number;
}

export interface RecordedRequest {
  method: string;
  path: string;
  body: unknown;
  lastEventId?: string;
  ifMatch?: string;
}

const RING = 1024;

/** Panel route prefixes and the capability that gates each (PUT and PATCH /v2/config need config.write instead). */
const PANEL_ROUTE_CAPS: [string, Capability][] = [
  ["/v2/config", "config.read"],
  ["/v2/backends", "config.write"],
  ["/v2/pats", "pats"],
  ["/v2/ingress", "ingress"],
  ["/v2/pair", "pair"],
  ["/v2/ci", "ci"],
  ["/v2/audit", "audit"],
];

export function randomEndpoint(): string {
  const tag = randomBytes(5).toString("hex");
  return process.platform === "win32"
    ? `\\\\.\\pipe\\xmesh-fakekernel-${tag}`
    : path.join(os.tmpdir(), `xmesh-fk-${tag}.sock`);
}

const iso = (): string => new Date().toISOString();

/**
 * A fake xmesh kernel for Control API v1: status, peers, sessions, drain,
 * resume and the event stream, over a Unix socket or named pipe like the real
 * one. Data starts from the contract fixtures; tests script it (`setStatus`,
 * `setPeers`, `emit`, `dropStreams`, `setControlVersion`, `restartBoot`) to
 * cover kernel-offline, version-mismatch, reconnect and warning cases.
 */
export class FakeKernel {
  readonly endpoint: string;
  playground?: FakePlayground;
  readonly kernelVersion: string;
  readonly requests: RecordedRequest[] = [];
  /** Data behind the panel routes (config, PATs, gateway, ingress, pairing, audit, sessions). */
  readonly panel: PanelData;
  /** Directory of the PAT token files; removed by `stop()`. */
  readonly tokenDir: string;
  private controlVersion: string;
  private status: StatusResponse;
  private peers: Peer[];
  private bootId: string;
  private readonly capabilities: Capability[];
  private readonly server: http.Server;
  private readonly streams = new Set<http.ServerResponse>();
  private ring: ControlEvent[] = [];
  private seq = 0;
  private ping: NodeJS.Timeout | undefined;
  private readonly pingMs: number;
  private readonly failures = new Map<string, { status: number; body: unknown }>();

  private constructor(opts: FakeKernelOptions) {
    this.endpoint = opts.endpoint ?? randomEndpoint();
    this.kernelVersion = opts.kernelVersion ?? "0.0.0-test";
    this.controlVersion = opts.controlVersion ?? CONTROL_API_VERSION;
    this.bootId = randomBytes(4).toString("hex");
    this.capabilities = opts.capabilities ?? statusFixture().control.capabilities;
    this.peers = opts.peers ?? peersFixture();
    this.status = { ...statusFixture(), ...opts.status };
    this.tokenDir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-fk-tok-"));
    const gw = this.status.gateway;
    this.panel = new PanelData(this.tokenDir, {
      listen: gw?.listen ?? ":8080",
      access: gw?.access ?? ["localhost", "lan", "tailnet"],
      hosts: gw?.hosts ?? [],
    });
    this.pingMs = opts.pingMs ?? 15000;
    this.refreshDerived();
    this.server = http.createServer((req, res) => this.handle(req, res));
    this.ping = setInterval(() => {
      for (const s of this.streams) s.write(": ping\n\n");
    }, this.pingMs);
    this.ping.unref();
  }

  static async start(opts: FakeKernelOptions = {}): Promise<FakeKernel> {
    const k = new FakeKernel(opts);
    if (process.platform !== "win32") fs.rmSync(k.endpoint, { force: true });
    await new Promise<void>((resolve, reject) => {
      k.server.once("error", reject);
      k.server.listen(k.endpoint, () => resolve());
    });
    if (opts.playground) {
      k.playground = new FakePlayground(() => k.panel.tokenOf("default"));
      await k.playground.start();
      k.status.roles = ["agent", "gateway"];
      k.refreshDerived();
    }
    return k;
  }

  get currentStatus(): StatusResponse {
    return this.status;
  }

  get connectedStreams(): number {
    return this.streams.size;
  }

  /** Merges `patch` into the status and publishes a `status` event. */
  setStatus(patch: Partial<StatusResponse>): void {
    this.status = { ...this.status, ...patch };
    this.refreshDerived();
    this.emit("status", this.status);
  }

  setWarnings(warnings: Warning[]): void {
    const was = this.status.warnings;
    this.status = { ...this.status, warnings };
    this.refreshDerived();
    for (const w of warnings) {
      if (!was.some((x) => x.code === w.code && x.message === w.message)) this.emit("warning", { change: "raised", warning: w });
    }
    for (const w of was) {
      if (!warnings.some((x) => x.code === w.code && x.message === w.message)) this.emit("warning", { change: "cleared", warning: w });
    }
    this.emit("status", this.status);
  }

  setPeers(peers: Peer[]): void {
    this.peers = peers;
    this.refreshDerived();
    this.emit("status", this.status);
  }

  /** Makes the next request for `path` answer with a ControlError. */
  failNext(path: string, status: number, error: string, message: string, extra: object = {}): void {
    this.failures.set(path, { status, body: { error, message, ...extra } });
  }

  setControlVersion(version: string): void {
    this.controlVersion = version;
  }

  /** Publishes an event with the next id. */
  emit(type: ControlEvent["type"], data: unknown): void {
    if (type === "hello" || type === "reset") throw new Error(`${type} is sent by the server per connection`);
    const seq = ++this.seq;
    const ev = { id: `${this.bootId}.${seq}`, seq, time: iso(), type, data } as ControlEvent;
    this.ring.push(ev);
    if (this.ring.length > RING) this.ring.shift();
    for (const s of this.streams) this.write(s, ev);
  }

  /** Ends every open event stream while the server keeps answering requests. */
  dropStreams(): void {
    for (const s of this.streams) s.end();
    this.streams.clear();
  }

  /** Pretends the kernel process restarted: new boot id, empty event history, streams cut. */
  restartBoot(): void {
    this.bootId = randomBytes(4).toString("hex");
    this.seq = 0;
    this.ring = [];
    this.status = { ...this.status, control: { ...this.status.control, boot_id: this.bootId } };
    this.dropStreams();
  }

  /** Drops the oldest events so a client resuming from them gets `reset: gap`. */
  forgetEvents(): void {
    this.ring = [];
  }

  manifest(): ControlEndpointFile {
    return {
      api_version: this.controlVersion,
      kernel_version: this.kernelVersion,
      node: this.status.hostname,
      // The test process hosts the fake kernel; publish its PID for process liveness checks.
      pid: process.pid,
      boot_id: this.bootId,
      transport: process.platform === "win32" ? "pipe" : "unix",
      endpoint: this.endpoint,
      started_at: this.status.started_at,
      capabilities: this.capabilities,
    };
  }

  /** Writes `<root>/<id>/control.json` the way the kernel does (temp file, then rename). */
  writeManifest(root: string, id: string): string {
    const dir = path.join(root, id);
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, "control.json");
    const tmp = `${file}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(this.manifest()));
    fs.renameSync(tmp, file);
    return file;
  }

  async stop(): Promise<void> {
    await this.playground?.stop();
    clearInterval(this.ping);
    this.dropStreams();
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
    if (process.platform !== "win32") fs.rmSync(this.endpoint, { force: true });
    fs.rmSync(this.tokenDir, { recursive: true, force: true });
  }

  /** A config or environment change can change the backends the status lists: derive them again and tell the streams if they differ. */
  private publishBackends(): void {
    const before = JSON.stringify(this.status.backends);
    this.refreshDerived();
    if (JSON.stringify(this.status.backends) !== before) this.emit("status", this.status);
  }

  private refreshDerived(): void {
    const others = this.peers.filter((p) => !p.self);
    this.status = {
      ...this.status,
      // The gateway block always follows the reach the panel data holds, as the kernel's status does.
      ...(this.status.gateway !== undefined && { gateway: this.panel.gatewayInfo() }),
      ...(this.playground && { gateway: { ...this.panel.gatewayInfo(), url: this.playground.url, addresses: [{ kind: "localhost" as const, host: "127.0.0.1", url: this.playground.url }] } }),
      // So do the backends: the configured ones, with the names (never the values) of their environment variables.
      backends: this.panel.backendRows(),
      control: {
        ...this.status.control,
        api_version: this.controlVersion,
        kernel_version: this.kernelVersion,
        boot_id: this.bootId,
        capabilities: this.capabilities,
      },
      peers: { total: others.length, online: others.filter((p) => p.online).length },
    };
  }

  private head(): string | null {
    return this.seq === 0 ? null : `${this.bootId}.${this.seq}`;
  }

  private write(res: http.ServerResponse, ev: ControlEvent): void {
    const hasId = ev.type !== "hello" && ev.type !== "reset";
    res.write(`${hasId ? `id: ${(ev as { id: string }).id}\n` : ""}event: ${ev.type}\ndata: ${JSON.stringify(ev)}\n\n`);
  }

  private send(res: http.ServerResponse, code: number, body: unknown): void {
    res.writeHead(code, {
      "Content-Type": "application/json",
      [CONTROL_VERSION_HEADER]: this.controlVersion,
    });
    res.end(JSON.stringify(body));
  }

  private error(res: http.ServerResponse, code: number, error: string, message: string, extra: object = {}): void {
    this.send(res, code, { error, message, ...extra });
  }

  private handle(req: http.IncomingMessage, res: http.ServerResponse): void {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const text = Buffer.concat(chunks).toString("utf8");
      let body: unknown = null;
      if (text !== "") {
        try {
          body = JSON.parse(text);
        } catch {
          body = text;
        }
      }
      const url = req.url ?? "/";
      const lastEventId = typeof req.headers["last-event-id"] === "string" ? req.headers["last-event-id"] : undefined;
      const ifMatch = typeof req.headers["if-match"] === "string" ? req.headers["if-match"] : undefined;
      const rec: RecordedRequest = { method: req.method ?? "GET", path: url, body };
      if (lastEventId !== undefined) rec.lastEventId = lastEventId;
      if (ifMatch !== undefined) rec.ifMatch = ifMatch;
      this.requests.push(rec);
      if (req.headers.origin !== undefined) {
        return this.error(res, 403, "forbidden", "带 Origin 的请求被拒绝");
      }
      this.route(rec.method, url, body, lastEventId, res, ifMatch);
    });
  }

  private requires(res: http.ServerResponse, capability: Capability): boolean {
    if (this.capabilities.includes(capability)) return true;
    this.error(res, 501, "not_implemented", `此内核尚未实现 ${capability}`);
    return false;
  }

  private route(method: string, url: string, body: unknown, lastEventId: string | undefined, res: http.ServerResponse, ifMatch?: string): void {
    const failure = this.failures.get(url);
    if (failure) {
      this.failures.delete(url);
      return this.send(res, failure.status, failure.body);
    }
    if (method === "GET" && url === "/v2/status") {
      if (this.requires(res, "status")) this.send(res, 200, this.status);
      return;
    }
    const revoke = /^\/v2\/pair\/trust\/(dev-[0-9a-f]{64})$/.exec(url);
    if (method === "DELETE" && revoke) {
      if (!this.requires(res, "pair")) return;
      if (!this.peers.some((p) => p.device_id === revoke[1] && !p.self)) return this.send(res, 404, { error: "not_found", message: "Device is not paired" });
      this.setPeers(this.peers.filter((p) => p.device_id !== revoke[1]));
      res.writeHead(204, { [CONTROL_VERSION_HEADER]: this.controlVersion }); res.end(); return;
    }
    if (method === "GET" && url === "/v2/peers") {
      if (this.requires(res, "peers")) this.send(res, 200, { peers: this.peers });
      return;
    }
    if (method === "GET" && url === "/v2/sessions") {
      if (this.requires(res, "sessions")) this.send(res, 200, this.panel.sessions);
      return;
    }
    if (method === "GET" && url === "/v2/events") {
      if (this.requires(res, "events")) this.openStream(res, lastEventId);
      return;
    }
    if (method === "POST" && url === "/v2/drain") {
      if (this.requires(res, "drain")) this.drain(res, body);
      return;
    }
    if (method === "POST" && url === "/v2/resume") {
      if (this.requires(res, "drain")) this.resume(res);
      return;
    }
    if (method === "PUT" && url === "/v2/gateway") {
      if (this.requires(res, "gateway")) this.putGateway(res, body);
      return;
    }
    const cap = PANEL_ROUTE_CAPS.find(([prefix]) => url.split("?")[0]!.startsWith(prefix));
    if (cap && this.requires(res, method !== "GET" && cap[0] === "/v2/config" ? "config.write" : cap[1])) {
      const r = this.panel.handle(method, url, body, ifMatch);
      if (r) {
        if (method !== "GET" && r.status < 300 && (url.startsWith("/v2/config") || url.startsWith("/v2/backends"))) this.publishBackends();
        if (r.status === 204) {
          res.writeHead(204, { [CONTROL_VERSION_HEADER]: this.controlVersion });
          res.end();
        } else {
          res.writeHead(r.status, { "Content-Type": "application/json", [CONTROL_VERSION_HEADER]: this.controlVersion, ...r.headers });
          res.end(JSON.stringify(r.body));
        }
        return;
      }
    } else if (cap) {
      return;
    }
    this.error(res, 404, "not_found", "没有这个接口");
  }

  /** RUL-16: repeating keep is a no-op; keep may be upgraded to kill; kill cannot be downgraded. */
  private drain(res: http.ServerResponse, body: unknown): void {
    const allowed = body === null ? [] : Object.keys(body as object);
    if (allowed.some((k) => k !== "mode")) return this.error(res, 400, "invalid_args", "未知字段", { field: allowed.find((k) => k !== "mode") });
    const mode = (body as { mode?: unknown } | null)?.mode ?? "keep";
    if (mode !== "keep" && mode !== "kill") return this.error(res, 400, "invalid_args", "mode 必须是 keep 或 kill", { field: "mode" });
    const cur = this.status.drain;
    if (cur.draining && cur.mode === "kill" && mode === "keep") {
      return this.error(res, 409, "conflict", "kill 不能降级为 keep，请先 resume", { reason: "drain_downgrade" });
    }
    let drain: DrainState = cur;
    if (!cur.draining || cur.mode !== mode) {
      drain = { draining: true, mode, since: cur.draining ? cur.since : iso() };
      this.status = { ...this.status, drain };
      this.emit("lifecycle", { change: "draining", drain });
      this.emit("status", this.status);
    }
    const out: DrainResponse = { drain, closed_pty: 0, released_leases: 0 };
    this.send(res, 200, out);
  }

  /** A listen change needs a restart and is reported as such (CTR-CTL-13); access and hosts apply at once. */
  private putGateway(res: http.ServerResponse, body: unknown): void {
    if (!this.status.roles.includes("gateway")) return this.error(res, 409, "role_disabled", "本机没有启用 gateway 角色");
    const r = this.panel.putGateway(body);
    if (r.status === 200) {
      const restart = (r.body as { restart_required: boolean }).restart_required;
      this.status = { ...this.status, restart_required: this.status.restart_required || restart };
      this.refreshDerived();
      this.emit("status", this.status);
    }
    this.send(res, r.status, r.body);
  }

  private resume(res: http.ServerResponse): void {
    const drain: DrainState = { draining: false, mode: null, since: null };
    if (this.status.drain.draining) {
      this.status = { ...this.status, drain };
      this.emit("lifecycle", { change: "resumed", drain });
      this.emit("status", this.status);
    }
    const out: DrainResponse = { drain, closed_pty: 0, released_leases: 0 };
    this.send(res, 200, out);
  }

  private openStream(res: http.ServerResponse, lastEventId: string | undefined): void {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-store",
      [CONTROL_VERSION_HEADER]: this.controlVersion,
    });
    res.write(": open\n\n");
    this.write(res, {
      type: "hello",
      time: iso(),
      data: { boot_id: this.bootId, head: this.head(), heartbeat_s: Math.max(1, Math.ceil(this.pingMs / 1000)) },
    });
    this.streams.add(res);
    res.on("close", () => this.streams.delete(res));
    if (lastEventId === undefined) return;

    const dot = lastEventId.lastIndexOf(".");
    const boot = dot === -1 ? "" : lastEventId.slice(0, dot);
    const seq = dot === -1 ? NaN : Number(lastEventId.slice(dot + 1));
    if (boot !== this.bootId) {
      return this.write(res, { type: "reset", time: iso(), data: { reason: "boot_changed", head: this.head() } });
    }
    const oldest = this.ring[0] as (ControlEvent & { seq: number }) | undefined;
    if (!Number.isFinite(seq) || (oldest !== undefined && seq < oldest.seq - 1) || (oldest === undefined && seq < this.seq)) {
      return this.write(res, { type: "reset", time: iso(), data: { reason: "gap", head: this.head() } });
    }
    for (const ev of this.ring) if ((ev as { seq: number }).seq > seq) this.write(res, ev);
  }
}
