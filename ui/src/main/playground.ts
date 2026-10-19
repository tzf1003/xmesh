import { Client, StreamableHTTPClientTransport, McpError, ToolListChangedNotificationSchema } from "./mcp-sdk";
import type { ShellController } from "./controller";
import { GatewayCredentials } from "./gateway-token";
import type { DataResult } from "../shared/ipc";
import type { CallOutcome, PlaygroundRequest, PlaygroundResult, ToolDescriptor, ToolResult } from "../shared/playground";

type Controller = Pick<ShellController, "snapshot" | "requireCapable">;

/** Only a loopback URL reported by the selected kernel may receive its credential. */
export function localGatewayUrl(raw: string): URL {
  const u = new URL(raw);
  if (u.protocol !== "http:" || !["127.0.0.1", "[::1]", "localhost"].includes(u.hostname) || u.pathname !== "/mcp" || u.username || u.password || u.search || u.hash) throw new Error("Local Gateway address is unavailable");
  return u;
}

export class PlaygroundService {
  private readonly connections = new Map<string, { key: string; client: Client; transport: StreamableHTTPClientTransport }>();
  private readonly connecting = new Map<string, Promise<Client>>();
  private epoch = 0;
  private revision = 0;
  private readonly waiting = new Map<string, { cancelled: boolean }>();
  private readonly running = new Map<string, AbortController>();
  private readonly progress: Record<string, { progress: number; total?: number; message?: string }> = Object.create(null) as Record<string, { progress: number; total?: number; message?: string }>;
  constructor(private readonly controller: Controller, private readonly desktop?: { copy(text: string): void; save(text: string): Promise<boolean> }) {}

  async close(cancelPending = true): Promise<void> {
    this.epoch++;
    if (cancelPending) for (const pending of this.waiting.values()) pending.cancelled = true;
    for (const c of this.running.values()) c.abort();
    this.running.clear();
    for (const k of Object.keys(this.progress)) delete this.progress[k];
    const old = [...this.connections.values()];
    this.connections.clear();
    await Promise.all(old.map(async (old) => {
      await old.transport.terminateSession().catch(() => undefined);
      await old.client.close().catch(() => undefined);
    }));
  }

  private async closeCredential(slot: string): Promise<void> {
    const connection=this.connections.get(slot);
    this.connections.delete(slot);
    if (connection){
      await connection.transport.terminateSession().catch(()=>undefined);
      await connection.client.close().catch(()=>undefined);
    }
  }

  private check(instance: string): void {
    const s = this.controller.snapshot;
    if (s.selectedInstance !== instance || s.connection.kind !== "connected") throw new Error("Selected node is no longer connected");
    if (!s.connection.status.roles.includes("gateway")) throw new Error("Enable the Gateway role to use the playground");
  }

  private async client(instance: string, pat?: string): Promise<Client> {
    this.check(instance);
    const slot = pat ?? "";
    const pending = this.connecting.get(slot);
    if (pending) { await pending; this.check(instance); }
    const c = this.controller.snapshot.connection;
    if (c.kind !== "connected") throw new Error("Gateway is unavailable");
    const raw = c.status.gateway?.addresses?.find((a) => a.kind === "localhost")?.url ?? c.status.gateway?.url;
    if (!raw) throw new Error("Local Gateway address is unavailable");
    const url = localGatewayUrl(raw);
    const epoch = this.epoch;
    const token = await new GatewayCredentials(this.controller).read(pat);
    this.check(instance);
    if (epoch !== this.epoch) throw new Error("Gateway connection changed");
    const key = JSON.stringify([instance, c.status.control.boot_id, url.href, token]);
    const current = this.connections.get(slot);
    if (current?.key === key) return current.client;
    const connecting = this.connecting.get(slot);
    if (connecting) { await connecting; return this.client(instance, pat); }
    if (!current && this.connections.size + this.connecting.size >= 8) throw new Error("Too many Gateway credentials are connected");
    const opening = (async () => {
      this.connections.delete(slot);
      if (current) {
        await current.transport.terminateSession().catch(() => undefined);
        await current.client.close().catch(() => undefined);
      }
      if (epoch !== this.epoch) throw new Error("Gateway connection changed");
      const generation = epoch;
      const client = new Client({ name: "xmesh-panel", version: "1.0.0" }, { capabilities: {} });
      const transport = new StreamableHTTPClientTransport(url, { requestInit: { headers: { Authorization: `Bearer ${token}`, "X-Xmesh-Origin": "panel" }, redirect: "error" } });
      client.setNotificationHandler(ToolListChangedNotificationSchema, () => { this.revision++; });
      try {
        await client.connect(transport, { timeout: 10_000 });
        this.check(instance);
        if (generation !== this.epoch) throw new Error("Gateway connection changed");
      } catch (e) { await client.close().catch(() => undefined); throw e; }
      client.onclose = () => { if (this.connections.get(slot)?.client === client) this.connections.delete(slot); };
      this.connections.set(slot, { key, client, transport });
      this.revision++;
      return client;
    })();
    this.connecting.set(slot, opening);
    try { return await opening; } finally { if (this.connecting.get(slot) === opening) this.connecting.delete(slot); }
  }

  async call(instance: string, id: string, name: string, args: Record<string, unknown>, timeoutMs: number, pat?: string): Promise<CallOutcome> {
    if (this.waiting.has(id) || this.running.has(id) || this.waiting.size + this.running.size >= 8) throw new Error("Too many calls or duplicate call id");
    const pending = { cancelled: false };
    this.waiting.set(id, pending);
    const started = Date.now();
    try {
      const client = await this.client(instance, pat);
      if (pending.cancelled) throw new Error("Call cancelled");
      this.waiting.delete(id);
      const abort = new AbortController();
      this.running.set(id, abort);
      const result = await client.callTool({ name, arguments: args }, undefined, {
        signal: abort.signal, timeout: timeoutMs, maxTotalTimeout: timeoutMs,
        onprogress: (p) => { this.progress[id] = { progress: p.progress, total: p.total, message: p.message }; },
      });
      return { result: result as ToolResult, durationMs: Date.now() - started };
    } catch (e) {
      if ((e as { code?: number }).code === 404 || (e instanceof Error && /session.*(not found|expired|invalid)/i.test(e.message))) await this.closeCredential(pat ?? "");
      throw e;
    } finally { this.waiting.delete(id); this.running.delete(id); delete this.progress[id]; }
  }

  async request<R extends PlaygroundRequest>(req: R): Promise<DataResult<PlaygroundResult<R>>> {
    try {
      this.check(req.instance);
      let data: unknown;
      switch (req.op) {
        case "copy": if (!this.desktop) throw new Error("Clipboard unavailable"); this.desktop.copy(req.text); data = { done: true }; break;
        case "save": if (!this.desktop) throw new Error("Save dialog unavailable"); data = { done: await this.desktop.save(req.text) }; break;
        case "cancel": {
          const pending = this.running.get(req.id);
          pending?.abort();
          const queued = this.waiting.get(req.id);
          if (queued) queued.cancelled = true;
          data = { cancelled: pending !== undefined || queued !== undefined };
          break;
        }
        case "status": data = { revision: this.revision, progress: { ...this.progress } }; break;
        case "call": data = await this.call(req.instance, req.id, req.name, req.arguments, req.timeoutMs, req.pat); break;
        case "list": {
          const client = await this.client(req.instance);
          if (req.device === "gateway") {
            const tools: ToolDescriptor[] = [];
            let cursor: string | undefined;
            do {
              const page = await client.listTools({ cursor });
              tools.push(...page.tools.filter((t) => !t.name.includes("/")) as ToolDescriptor[]);
              cursor = page.nextCursor;
            } while (cursor);
            data = { tools, truncated: false, revision: this.revision };
          } else {
            const out = await client.callTool({ name: "tools.search", arguments: { device: req.device, pattern: req.pattern ?? "*" } }) as ToolResult;
            const raw: Record<string, unknown> = out.structuredContent ?? JSON.parse(out.content.filter((c) => c.type === "text").map((c) => c.text).join("")) as Record<string, unknown>;
            if (out.isError) {
              const error = new Error(String(raw.message ?? JSON.stringify(raw))) as Error & { code: string };
              error.code = typeof raw.error === "string" ? raw.error : "tool_error";
              throw error;
            }
            data = { tools: raw.tools ?? [], truncated: raw.truncated === true, revision: this.revision };
          }
          break;
        }
      }
      return { ok: true, data: data as PlaygroundResult<R> };
    } catch (e) {
      let code = typeof (e as { code?: unknown })?.code === "string" ? (e as { code: string }).code : "playground_error";
      if (e instanceof Error && e.name === "TokenUnavailableError") code = "token_unavailable";
      if (e instanceof McpError) code = e.code === -32001 ? "timeout" : "protocol_error";
      if (e instanceof Error && /abort|cancel/i.test(e.message)) code = "cancelled";
      return { ok: false, error: { code, message: e instanceof Error ? e.message : String(e) } };
    }
  }
}
