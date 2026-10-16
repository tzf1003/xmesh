import { createHash, randomBytes } from "node:crypto";
import type {
  AuditEvent,
  AuditResponse,
  ConfigChange,
  ConfigDiagnostic,
  ConfigPutResponse,
  ConfigResponse,
  IngressClient,
  PairDocument,
  PatInfo,
  SessionsResponse,
} from "../src/shared/control";
import { fixture } from "./fixtures";

// The panel's data routes for the fake kernel: config, PATs, ingress, pairing,
// audit and sessions. State lives in memory; the shapes come from the contract
// fixtures. The behaviour that tests rely on is deliberately simple:
//
//   - config content containing "BROKEN" fails validation (422 config_invalid);
//   - content containing "allow_dangerous: true" produces a diff entry with weakens_security;
//   - PUT /v1/config without If-Match answers 428, with a stale one 412 + current_etag.

export interface RouteResult {
  status: number;
  body?: unknown;
  /** Extra response headers (the config `ETag`). */
  headers?: Record<string, string>;
}

const iso = (): string => new Date().toISOString();
const ok = (body: unknown, status = 200, headers?: Record<string, string>): RouteResult => ({ status, body, ...(headers && { headers }) });
const err = (status: number, error: string, message: string, extra: object = {}): RouteResult => ({ status, body: { error, message, ...extra } });

export function etagOf(content: string): string {
  return `sha256-${createHash("sha256").update(content, "utf8").digest("hex")}`;
}

export class PanelData {
  sessions: SessionsResponse = fixture("SessionsResponse.busy");
  private diskContent: string;
  private effectiveEtag: string;
  pats: PatInfo[] = fixture<{ pats: PatInfo[] }>("PatList.one").pats;
  patsEnforced = true;
  clients: IngressClient[] = fixture<{ clients: IngressClient[] }>("IngressClientsResponse.two").clients;
  issuer: string | null = "https://win-dev-01.tail1234.ts.net";
  localPair: PairDocument = fixture<{ pair: PairDocument }>("PairExportResponse.ok").pair;
  imported: { name: string; tags: string[] }[] = [];
  audit: AuditEvent[] = fixture<AuditResponse>("AuditResponse.page").events;
  auditEnabled = true;
  /** Config writes that reached the "disk". */
  writes: string[] = [];
  /** Passwords are write-only; the fake only counts them. */
  passwordRotations = 0;
  /** `lan` makes pairing available; anything else answers unsupported_mode. */
  mode: "lan" | "tailnet" | "local" = "lan";
  hasIngress = true;

  constructor() {
    this.diskContent = fixture<ConfigResponse>("ConfigResponse.valid").content;
    this.effectiveEtag = etagOf(this.diskContent);
  }

  get content(): string {
    return this.diskContent;
  }

  setContent(content: string, effective = true): void {
    this.diskContent = content;
    if (effective) this.effectiveEtag = etagOf(content);
  }

  handle(method: string, url: string, body: unknown, ifMatch: string | undefined): RouteResult | undefined {
    const u = new URL(url, "http://x");
    const p = u.pathname;
    if (method === "GET" && p === "/v1/sessions") return ok(this.sessions);
    if (method === "GET" && p === "/v1/config") return this.getConfig();
    if (method === "PUT" && p === "/v1/config") return this.putConfig(body, ifMatch);
    if (p === "/v1/pats" || p.startsWith("/v1/pats/")) return this.pats_(method, p, body);
    if (p.startsWith("/v1/ingress/")) return this.ingress(method, p, body);
    if (p.startsWith("/v1/pair/")) return this.pair(method, p, body);
    if (method === "GET" && p === "/v1/audit") return this.auditQuery(u.searchParams);
    return undefined;
  }

  private getConfig(): RouteResult {
    const etag = etagOf(this.diskContent);
    const valid = !this.diskContent.includes("BROKEN");
    const res: ConfigResponse = {
      etag,
      content: this.diskContent,
      parsed: valid ? { node: { name: "win-dev-01", roles: ["agent"] } } : null,
      valid,
      load_error: valid ? null : { message: "yaml: 缩进错误", line: 3, column: 1 },
      effective_etag: this.effectiveEtag,
      restart_required: this.effectiveEtag !== etag,
    };
    return ok(res, 200, { ETag: `"${etag}"` });
  }

  private putConfig(body: unknown, ifMatch: string | undefined): RouteResult {
    const b = body as { content?: unknown; dry_run?: unknown } | null;
    if (typeof b?.content !== "string") return err(400, "invalid_args", "缺少 content", { field: "content" });
    if (ifMatch === undefined) return err(428, "precondition_required", "必须带 If-Match");
    const current = etagOf(this.diskContent);
    if (ifMatch.replaceAll('"', "") !== current) return err(412, "precondition_failed", "配置已被修改", { current_etag: current });
    if (b.content.includes("BROKEN")) {
      const diagnostics: ConfigDiagnostic[] = [
        { pointer: "/agent/pty/max_sessions", message: "必须是正整数" },
        { line: 3, column: 1, message: "yaml: 缩进错误" },
      ];
      return err(422, "config_invalid", "配置未通过校验", { diagnostics });
    }
    // The generated type says before/after are objects, but the schema allows any JSON value.
    const diff: ConfigChange[] = (
      b.content === this.diskContent
        ? []
        : [
            {
              pointer: "/agent/allow_dangerous",
              op: "replace",
              before: false,
              after: b.content.includes("allow_dangerous: true"),
              restart_required: false,
              weakens_security: b.content.includes("allow_dangerous: true"),
            },
            ...(b.content.includes("mode: tailnet")
              ? [{ pointer: "/mesh/mode", op: "replace" as const, before: "lan", after: "tailnet", restart_required: true, weakens_security: false }]
              : []),
          ]) as unknown as ConfigChange[];
    const restart = diff.some((d) => d.restart_required);
    const apply = b.dry_run !== true && diff.length > 0;
    if (apply) {
      this.writes.push(b.content);
      this.diskContent = b.content;
      if (!restart) this.effectiveEtag = etagOf(b.content);
    }
    const res: ConfigPutResponse = { applied: apply, etag: etagOf(this.diskContent), diff, restart_required: restart };
    return ok(res);
  }

  private pats_(method: string, p: string, body: unknown): RouteResult {
    if (method === "GET" && p === "/v1/pats") return ok({ pats: this.pats, enforced: this.patsEnforced });
    if (method === "POST" && p === "/v1/pats") {
      const b = body as { name?: string; scopes?: string[] } | null;
      if (!b?.name || !Array.isArray(b.scopes)) return err(400, "invalid_args", "name 和 scopes 必填");
      if (this.pats.some((x) => x.name === b.name)) return err(409, "conflict", "名字已存在");
      const info: PatInfo = {
        name: b.name,
        scopes: b.scopes as PatInfo["scopes"],
        token_file: `C:\\Users\\x\\.xmesh\\${b.name}.pat`,
        modified_at: iso(),
      };
      this.pats = [...this.pats, info];
      this.patsEnforced = true;
      return ok({ pat: info, token: randomBytes(24).toString("base64url") }, 201);
    }
    const m = /^\/v1\/pats\/([^/]+)(\/rotate)?$/.exec(p);
    if (m) {
      const name = decodeURIComponent(m[1]!);
      const i = this.pats.findIndex((x) => x.name === name);
      if (i === -1) return err(404, "not_found", "没有这个 PAT");
      if (method === "POST" && m[2]) {
        const info: PatInfo = { ...this.pats[i]!, modified_at: iso() };
        this.pats = this.pats.map((x, j) => (j === i ? info : x));
        return ok({ pat: info, token: randomBytes(24).toString("base64url") });
      }
      if (method === "DELETE" && !m[2]) {
        this.pats = this.pats.filter((_, j) => j !== i);
        this.patsEnforced = this.pats.length > 0;
        return { status: 204 };
      }
    }
    return err(404, "not_found", "没有这个接口");
  }

  private ingress(method: string, p: string, body: unknown): RouteResult {
    if (!this.hasIngress) return err(409, "role_disabled", "本机没有启用 ingress 角色");
    if (method === "GET" && p === "/v1/ingress/clients") return ok({ issuer: this.issuer, clients: this.clients });
    const m = /^\/v1\/ingress\/clients\/([^/]+)$/.exec(p);
    if (method === "DELETE" && m) {
      const id = decodeURIComponent(m[1]!);
      if (!this.clients.some((c) => c.client_id === id)) return err(404, "not_found", "没有这个客户端");
      this.clients = this.clients.filter((c) => c.client_id !== id);
      return { status: 204 };
    }
    if (method === "PUT" && p === "/v1/ingress/password") {
      const pw = (body as { password?: unknown } | null)?.password;
      if (typeof pw !== "string" || pw.length < 12 || pw.length > 256) return err(400, "invalid_args", "口令长度需为 12 到 256", { field: "password" });
      this.passwordRotations++;
      return ok({ rotated_at: iso() });
    }
    return err(404, "not_found", "没有这个接口");
  }

  private pair(method: string, p: string, body: unknown): RouteResult {
    if (this.mode !== "lan") return err(409, "unsupported_mode", "只有 lan 模式支持配对");
    if (method === "POST" && p === "/v1/pair/export") return ok({ pair: this.localPair });
    if (method === "POST" && p === "/v1/pair/import") {
      const b = body as { pair?: PairDocument; fingerprint?: string; tags?: string[] } | null;
      if (!b?.pair || typeof b.fingerprint !== "string") return err(400, "invalid_args", "缺少 pair 或 fingerprint");
      const norm = (s: string): string => s.replaceAll(":", "").toLowerCase();
      if (norm(b.fingerprint) !== norm(b.pair.fingerprint)) return err(422, "fingerprint_mismatch", "指纹与证书不符");
      const tags = b.tags ?? ["tag:peer", "tag:device"];
      this.imported.push({ name: b.pair.name, tags });
      return ok({ name: b.pair.name, fingerprint: b.pair.fingerprint, tags, restart_required: true });
    }
    return err(404, "not_found", "没有这个接口");
  }

  private auditQuery(q: URLSearchParams): RouteResult {
    if (!this.auditEnabled) return ok({ enabled: false, events: [], next_cursor: null } satisfies AuditResponse);
    let events = this.audit;
    const tool = q.get("tool");
    const status = q.get("status");
    const event = q.get("event");
    if (tool) events = events.filter((e) => e.tool === tool);
    if (status) events = events.filter((e) => e.status === status);
    if (event) events = events.filter((e) => e.event === event);
    const limit = Number(q.get("limit") ?? 100);
    return ok({ enabled: true, events: events.slice(0, limit), next_cursor: null } satisfies AuditResponse);
  }
}
