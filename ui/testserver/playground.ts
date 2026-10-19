import { FakeWorkspace } from "./workspace";
import { FakeComputer } from "./computer";
import * as http from "node:http";
import { randomUUID } from "node:crypto";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import type { ToolDescriptor } from "../src/shared/playground";

/** A protocol-speaking test Gateway; the panel uses its production MCP client against it. */
export class FakePlayground {
  url = "";
  workspace?: FakeWorkspace;
  desktop?: FakeComputer;
  enableDesktop(): FakeComputer { this.desktop = new FakeComputer(); this.tools.push(...this.desktop.tools()); return this.desktop; }
  enableWorkspace(): FakeWorkspace { this.workspace = new FakeWorkspace(); this.tools.push(...this.workspace.tools()); return this.workspace; }
  readonly calls: { name: string; args: Record<string, unknown>; origin?: string }[] = [];
  readonly sessions = new Map<string, { transport: StreamableHTTPServerTransport; server: Server }>();
  tools: ToolDescriptor[] = [
    { name: "local/sys.info", description: "System information", inputSchema: { type: "object", additionalProperties: false }, annotations: { readOnlyHint: true } },
    { name: "local/exec.run", description: "Run a command", inputSchema: { type: "object", required: ["cmd"], properties: { cmd: { type: "string", minLength: 1 }, args: { type: "array", items: { type: "string" } }, cwd: { type: "string" }, stdin: { type: "string" }, env: { type: "object" }, timeout: { type: "string" } } } },
    { name: "local/demo.echo", description: "Echo typed arguments", inputSchema: { type: "object", required: ["message"], properties: { message: { type: "string" }, format: { type: "string", enum: ["text", "json"] }, repeat: { type: "integer", minimum: 1 }, enabled: { type: "boolean" }, metadata: { type: "object" }, tags: { type: "array" } } } },
    { name: "local/demo.denied", description: "Policy denial", inputSchema: { type: "object" } },
    { name: "local/demo.slow", description: "Slow operation", inputSchema: { type: "object" } },
  ];
  private http = http.createServer((req, res) => { void this.handle(req, res).catch(() => { if (!res.headersSent) res.writeHead(500); res.end(); }); });
  constructor(private readonly token: () => string | undefined) {}
  async start(): Promise<void> {
    await new Promise<void>((resolve) => this.http.listen(0, "127.0.0.1", resolve));
    this.url = `http://127.0.0.1:${(this.http.address() as { port: number }).port}/mcp`;
  }
  async changed(): Promise<void> { await Promise.all([...this.sessions.values()].map((s) => s.server.sendToolListChanged())); }
  async stop(): Promise<void> {
    await Promise.all([...this.sessions.values()].map((s) => s.transport.close()));
    this.http.closeAllConnections();
    await new Promise<void>((resolve) => this.http.close(() => resolve()));
  }
  private async handle(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    if (req.headers.authorization !== `Bearer ${this.token()}`) { res.writeHead(401).end(); return; }
    const session = req.headers["mcp-session-id"];
    const existing = typeof session === "string" ? this.sessions.get(session) : undefined;
    if (existing) { await existing.transport.handleRequest(req, res); return; }
    if (req.method !== "POST") { res.writeHead(404).end(); return; }
    const origin = req.headers["x-xmesh-origin"] as string | undefined;
    const server = new Server({ name: "xmesh-fake-gateway", version: "1" }, { capabilities: { tools: { listChanged: true } } });
    server.setRequestHandler(ListToolsRequestSchema, () => ({ tools: [{ name: "tools.search", inputSchema: { type: "object" } }, ...this.tools] }));
    server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
      const name = request.params.name, args = request.params.arguments ?? {};
      this.calls.push({ name, args, origin });
      if (this.desktop && name.startsWith("local/computer.")) return this.desktop.call(name.slice("local/computer.".length), args);
      let body: Record<string, unknown>;
      let isError = false;
      if (name === "tools.search") {
        const prefix = String(args.device) + "/";
        body = { tools: this.tools.filter((t) => t.name.startsWith(prefix) && (args.pattern === "*" || `${t.name} ${t.description}`.toLowerCase().includes(String(args.pattern).toLowerCase()))), truncated: false };
      } else if (this.workspace && /\/(fs|pty)\./.test(name)) {
        body = await this.workspace.call(name.split("/").at(-1)!, args, transport.sessionId ?? "owner", extra.signal);
        isError = typeof body.error === "string";
      } else if (name.endsWith("/demo.denied")) { body = { error: "policy_denied", rule: "test.deny", message: "Denied by remote policy" }; isError = true; }
      else if (name.endsWith("/demo.slow")) {
        await extra.sendNotification({ method: "notifications/progress", params: { progressToken: request.params._meta?.progressToken ?? 0, progress: 1, total: 10, message: "working" } });
        await new Promise<void>((resolve, reject) => { const timer = setTimeout(resolve, 30_000); extra.signal.addEventListener("abort", () => { clearTimeout(timer); reject(new Error("cancelled")); }, { once: true }); });
        body = { done: true };
      } else if (name.endsWith("/exec.run")) body = { exit_code: 0, stdout: `executed: ${String(args.cmd)}\n`, stderr: "", duration_ms: 5, truncated: false };
      else if (name.endsWith("/sys.info")) body = { hostname: "fixture-device", os: "linux", arch: "amd64" };
      else body = args;
      return { content: [{ type: "text", text: JSON.stringify(body) }], structuredContent: body, isError };
    });
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: randomUUID, onsessioninitialized: (id) => { this.sessions.set(id, { transport, server }); } });
    transport.onclose = () => { if (transport.sessionId) this.sessions.delete(transport.sessionId); };
    await server.connect(transport);
    await transport.handleRequest(req, res);
  }
}
