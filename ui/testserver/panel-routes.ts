import { createHash, randomBytes } from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import { parse } from "yaml";
import type {
  AuditEvent,
  AuditResponse,
  Backend,
  ConfigChange,
  ConfigDiagnostic,
  ConfigPutResponse,
  ConfigResponse,
  GatewayAddress,
  GatewayInfo,
  GatewayUpdateResponse,
  IngressClient,
  PairConnectRequest,
  PairDiscoveredDevice,
  PairDocument,
  PairPasswordState,
  PatInfo,
  PatList,
  PatSecret,
  SessionsResponse,
} from "../src/shared/control";
import type { ConfigReloadResponse } from "../src/shared/ipc";
import { isAccessEntry, isGatewayHost, isListen, listenPort, listenScope } from "../src/shared/gateway";
import { fixture } from "./fixtures";

// The panel's data routes for the fake kernel: config, PATs, ingress, pairing,
// audit and sessions. State lives in memory; the shapes come from the contract
// fixtures. The behaviour that tests rely on is deliberately simple:
//
//   - config content containing "BROKEN" fails validation (422 config_invalid);
//   - content containing "allow_dangerous: true" produces a diff entry with weakens_security;
//   - PUT /v1/config without If-Match answers 428, with a stale one 412 + current_etag;
//   - PATCH /v1/config applies JSON Pointer edits to the parsed document (arrays are
//     replaced whole, missing maps are created, a scalar on the path is 409), all or
//     nothing, then writes the document back as JSON text (JSON is valid YAML). Comments
//     are not kept by the fake; the real kernel keeps them.
//   - PATs keep their token in a file under `tokenDir`, like the kernel's 0600 files: create,
//     rotate and delete rewrite or remove it, and nothing else ever returns the token body;
//   - PUT /v1/gateway validates the reach settings; the addresses follow from listen and access;
//   - PATCH and DELETE /v1/backends/{name}/env keep environment variables write-only, like the kernel's
//     private file: nothing the fake serves ever carries a value, only `envValues` (a test hook) reads one.
//     Loader and shell hijack names (LD_*, XMESH_* ...) answer 400 invalid_args with reason env_denied.

/** Addresses the fake reports for a LAN and a tailnet, so the panel has something to list. */
export const FAKE_LAN_IP = "192.168.1.20";
export const FAKE_TAILNET_IP = "100.101.102.103";
export const FAKE_TAILNET_NAME = "win-dev-01.tail1234.ts.net";

/** The reach settings the gateway answers with (what `gateway.listen`, `access` and `hosts` say). */
export interface GatewayReach {
  listen: string;
  access: string[];
  hosts: string[];
}

/** Names the kernel refuses for a backend whatever the operator says (the fake's copy of the hijack list). */
const HIJACK_ENV = /^(SSH_AUTH_SOCK|TS_AUTHKEY|TAILSCALE_.*|XMESH_.*|LD_.*|DYLD_.*|BASH_ENV|ENV|ZDOTDIR|PYTHONPATH|NODE_OPTIONS)$/i;
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const BACKEND_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const ENV_MAX_VARS = 64;
const ENV_MAX_VALUE_BYTES = 8192;

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

/** The PAT files the kernel keeps: one token per file, with a trailing newline like a hand-edited file. */
function writeTokenFile(file: string, token: string): void {
  const tmp = `${file}.tmp`;
  fs.writeFileSync(tmp, `${token}\n`, { mode: 0o600 });
  fs.renameSync(tmp, file);
}

export class PanelData {
  sessions: SessionsResponse = fixture("SessionsResponse.busy");
  private diskContent: string;
  /** Settings the kernel has taken a change to but not yet restarted for, as config pointers. */
  private restartPaths: string[] = [];
  private effectiveEtag: string;
  /** The parsed document `GET /v1/config` returns. PATCH edits it; a PUT adopts it only when the text is JSON. */
  private model: Record<string, unknown>;
  pats: PatInfo[];
  gateway: GatewayReach;
  clients: IngressClient[] = fixture<{ clients: IngressClient[] }>("IngressClientsResponse.two").clients;
  issuer: string | null = "https://win-dev-01.tail1234.ts.net";
  localPair: PairDocument = fixture<{ pair: PairDocument }>("PairExportResponse.ok").pair;
  imported: { name: string; tags: string[] }[] = [];
  /** This node's connection password, as far as the panel can tell: the fake keeps only the state. */
  pairPassword: PairPasswordState = { set: false, updated_at: null, failed_attempts: 0, locked_until: null };
  /** Unpaired devices the fake sees on the LAN. Connecting to one removes it from the list. */
  discovered: PairDiscoveredDevice[] = fixture<{ devices: PairDiscoveredDevice[] }>("PairDiscoveredResponse.two").devices;
  audit: AuditEvent[] = fixture<AuditResponse>("AuditResponse.page").events;
  auditEnabled = true;
  /** Config writes that reached the "disk". */
  writes: string[] = [];
  /** Environment variables of stdio backends: backend, then name, then value. Never served back. */
  private backendEnv = new Map<string, Map<string, string>>();
  /** Passwords are write-only; the fake only counts them. */
  passwordRotations = 0;
  /** `lan` makes pairing available; anything else answers unsupported_mode. */
  mode: "lan" | "tailnet" | "local" = "lan";
  hasIngress = true;

  constructor(
    readonly tokenDir: string,
    gateway: GatewayReach,
  ) {
    this.diskContent = fixture<ConfigResponse>("ConfigResponse.valid").content;
    this.effectiveEtag = etagOf(this.diskContent);
    this.model = (fixture<ConfigResponse>("ConfigResponse.valid").parsed ?? {}) as Record<string, unknown>;
    this.gateway = { listen: gateway.listen, access: [...gateway.access], hosts: [...gateway.hosts] };
    // The fixture PAT is kept, with its token file moved under the fake's own directory, and the kernel's
    // "default" PAT (the gateway token that rotation targets) beside it.
    const claude = fixture<{ pats: PatInfo[] }>("PatList.one").pats[0]!;
    this.pats = [
      this.issue(claude.name, claude.scopes),
      this.issue("default", ["devices:read", "exec:*", "fs:read:*"]),
    ];
  }

  /** Creates or replaces a PAT's token file and returns the PAT info for it. */
  private issue(name: string, scopes: string[]): PatInfo {
    const token = randomBytes(32).toString("base64url");
    const file = path.join(this.tokenDir, `${name}.pat`);
    writeTokenFile(file, token);
    return { name, scopes: scopes as PatInfo["scopes"], token_file: file, modified_at: iso() };
  }

  /** The token body of a PAT, for the e2e tests that check what the clipboard received. */
  tokenOf(name: string): string | undefined {
    const pat = this.pats.find((p) => p.name === name);
    return pat ? fs.readFileSync(pat.token_file, "utf8").trim() : undefined;
  }

  get content(): string {
    return this.diskContent;
  }

  setContent(content: string, effective = true): void {
    this.diskContent = content;
    if (effective) this.effectiveEtag = etagOf(content);
    this.adopt(content);
  }

  handle(method: string, url: string, body: unknown, ifMatch: string | undefined): RouteResult | undefined {
    const u = new URL(url, "http://x");
    const p = u.pathname;
    if (method === "GET" && p === "/v1/sessions") return ok(this.sessions);
    if (method === "GET" && p === "/v1/config") return this.getConfig();
    if (method === "PUT" && p === "/v1/config") return this.putConfig(body, ifMatch);
    if (method === "PATCH" && p === "/v1/config") return this.patchConfig(body, ifMatch);
    if (method === "POST" && p === "/v1/config/reload") return ok(this.reload());
    if (p === "/v1/pats" || p.startsWith("/v1/pats/")) return this.pats_(method, p, body);
    if (method === "PUT" && p === "/v1/gateway") return this.putGateway(body);
    const env = /^\/v1\/backends\/([^/]+)\/env$/.exec(p);
    if (env && (method === "PATCH" || method === "DELETE")) return this.backendEnvRoute(method, decodeURIComponent(env[1]!), body);
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
      parsed: valid ? (parse(this.diskContent) as ConfigResponse["parsed"]) : null,
      valid,
      load_error: valid ? null : { message: "yaml: 缩进错误", line: 3, column: 1 },
      effective_etag: this.effectiveEtag,
      restart_required: this.effectiveEtag !== etag,
    };
    return ok(res, 200, { ETag: `"${etag}"` });
  }

  /** The kernel applies what it can in place; what it cannot takes a restart, until one happens. */
  private reload(): ConfigReloadResponse {
    return {
      applied: true,
      valid: true,
      load_error: null,
      changed_paths: [],
      restart_required: this.restartPaths.length > 0,
      restart_paths: [...this.restartPaths],
    };
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
      semantic(b.content) === semantic(this.diskContent)
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
    // Like the kernel, a change that only touches comments or blank lines has an empty diff but is still written.
    const apply = b.dry_run !== true && b.content !== this.diskContent;
    if (apply) {
      this.restartPaths = [...new Set([...this.restartPaths, ...diff.filter((d) => d.restart_required).map((d) => d.pointer)])];
      this.writes.push(b.content);
      this.diskContent = b.content;
      if (!restart) this.effectiveEtag = etagOf(b.content);
      this.adopt(b.content);
      this.pruneBackendEnv();
    }
    const res: ConfigPutResponse = { applied: apply, etag: etagOf(this.diskContent), diff, restart_required: restart };
    return ok(res);
  }

  /** Same order of checks as PUT: body, then If-Match, then the document. Nothing is written unless every edit applies and the result validates. */
  private patchConfig(body: unknown, ifMatch: string | undefined): RouteResult {
    const b = body as { edits?: unknown; dry_run?: unknown } | null;
    if (!Array.isArray(b?.edits) || b.edits.length < 1 || b.edits.length > 64) {
      return err(400, "invalid_args", "edits 需要 1 到 64 项", { field: "edits" });
    }
    if (ifMatch === undefined) return err(428, "precondition_required", "必须带 If-Match");
    const current = etagOf(this.diskContent);
    if (ifMatch.replaceAll('"', "") !== current) return err(412, "precondition_failed", "配置已被修改", { current_etag: current });

    const doc = structuredClone(this.model);
    const diff: ConfigChange[] = [];
    for (const [i, raw] of b.edits.entries()) {
      const edit = raw as { path?: unknown; value?: unknown; delete?: unknown } | null;
      if (typeof edit?.path !== "string" || !edit.path.startsWith("/") || edit.path.length < 2) {
        return err(400, "invalid_args", `edits[${i}].path 必须是 JSON Pointer`, { field: `edits/${i}/path` });
      }
      const remove = edit.delete === true;
      if (!remove && !("value" in edit)) return err(400, "invalid_args", `edits[${i}] 缺少 value`, { field: `edits/${i}/value` });
      const segments = edit.path.slice(1).split("/").map((s) => s.replaceAll("~1", "/").replaceAll("~0", "~"));
      const before = pointerGet(doc, segments);
      const applied = remove ? pointerRemove(doc, segments) : pointerSet(doc, segments, edit.value);
      if (!applied) return err(409, "conflict", `edits[${i}] 无法应用到当前文件`, { field: `edits/${i}/path` });
      const op = remove ? "remove" : before === undefined ? "add" : "replace";
      diff.push({ pointer: edit.path, op, before: before ?? null, after: remove ? null : edit.value, restart_required: false, weakens_security: false } as unknown as ConfigChange);
    }

    const content = `${JSON.stringify(doc, null, 2)}\n`;
    if (content.includes("BROKEN")) {
      const diagnostics: ConfigDiagnostic[] = [{ pointer: "/agent/backends", message: "配置未通过校验" }];
      return err(422, "config_invalid", "配置未通过校验", { diagnostics });
    }
    const apply = b.dry_run !== true && content !== this.diskContent;
    if (apply) {
      this.writes.push(content);
      this.diskContent = content;
      this.model = doc;
      this.effectiveEtag = etagOf(content);
      this.pruneBackendEnv();
    }
    // Agent settings reload without a restart in the fake, as in the kernel.
    const res: ConfigPutResponse = { applied: apply, etag: etagOf(this.diskContent), diff, restart_required: false };
    return ok(res);
  }

  /** The backends the document defines, as the kernel's status lists them: the state is always running, env_names follow the private file. */
  backendRows(): Backend[] {
    const backends = pointerGet(this.model, ["agent", "backends"]);
    if (!isNode(backends)) return [];
    return Object.keys(backends)
      .sort()
      .map((name) => {
        const b = isNode(backends[name]) ? backends[name] : {};
        const names = b.transport === "stdio" ? this.envNames(name) : [];
        return {
          name,
          policy: b.policy === "exclusive" ? "exclusive" : "shared",
          state: "running" as const,
          favorite: b.favorite === true,
          ...(names.length > 0 && { env_names: names }),
        };
      });
  }

  /** The variable names of one backend, sorted. */
  envNames(name: string): string[] {
    return [...(this.backendEnv.get(name)?.keys() ?? [])].sort();
  }

  /** Test hook: the stored values of one backend. The panel never gets these from any route. */
  envValues(name: string): Record<string, string> {
    return Object.fromEntries(this.backendEnv.get(name) ?? []);
  }

  /** Like the kernel after a config save: a backend that is no longer in the document keeps no variables. */
  private pruneBackendEnv(): void {
    const backends = pointerGet(this.model, ["agent", "backends"]);
    for (const name of [...this.backendEnv.keys()]) {
      if (!isNode(backends) || !(name in backends)) this.backendEnv.delete(name);
    }
  }

  private backendEnvRoute(method: string, name: string, body: unknown): RouteResult {
    if (!BACKEND_NAME.test(name)) return err(400, "invalid_args", "后端名不合法", { field: "name" });
    if (method === "DELETE") {
      this.backendEnv.delete(name);
      return { status: 204 };
    }
    if (!isNode(body)) return err(400, "invalid_args", "请求体必须是对象", { field: "body" });
    const extra = Object.keys(body).find((k) => k !== "set" && k !== "unset");
    if (extra !== undefined) return err(400, "invalid_args", `未知字段 ${extra}`, { field: extra });
    const set = body.set === undefined ? {} : body.set;
    const unset = body.unset === undefined ? [] : body.unset;
    if (!isNode(set) || !Array.isArray(unset) || !unset.every((x) => typeof x === "string")) {
      return err(400, "invalid_args", "set 必须是对象，unset 必须是字符串数组", { field: "body" });
    }
    if (Object.keys(set).length === 0 && unset.length === 0) return err(400, "invalid_args", "set 和 unset 至少要有一项", { field: "body" });
    if (Object.keys(set).length > ENV_MAX_VARS || unset.length > ENV_MAX_VARS) return err(400, "invalid_args", "一次最多改 64 个变量", { field: "body" });
    for (const [k, v] of Object.entries(set).sort(([a], [b]) => a.localeCompare(b))) {
      if (!ENV_NAME.test(k)) return err(400, "invalid_args", "变量名不合法", { field: "set" });
      if (typeof v !== "string" || v.includes("\0") || Buffer.byteLength(v) > ENV_MAX_VALUE_BYTES) return err(400, "invalid_args", "变量值不合法", { field: `set.${k}` });
      if (HIJACK_ENV.test(k)) return err(400, "invalid_args", "节点策略不允许这个变量", { field: `set.${k}`, reason: "env_denied" });
    }
    for (const k of unset as string[]) {
      if (!ENV_NAME.test(k)) return err(400, "invalid_args", "变量名不合法", { field: "unset" });
      if (k in set) return err(400, "invalid_args", "同一个变量不能同时设置和删除", { field: "unset" });
    }
    const vars = new Map(this.backendEnv.get(name) ?? []);
    for (const k of unset as string[]) vars.delete(k);
    for (const [k, v] of Object.entries(set)) vars.set(k, v as string);
    if (vars.size > ENV_MAX_VARS) return err(400, "invalid_args", "一个后端最多 64 个变量", { field: "body" });
    if (vars.size === 0) this.backendEnv.delete(name);
    else this.backendEnv.set(name, vars);
    return ok({ name, names: this.envNames(name) });
  }

  /** A JSON object becomes the parsed document; other text (YAML) leaves the last parsed document in place. */
  private adopt(content: string): void {
    try {
      const v: unknown = JSON.parse(content);
      if (typeof v === "object" && v !== null && !Array.isArray(v)) this.model = v as Record<string, unknown>;
    } catch {
      // Not JSON: keep the model.
    }
  }

  private pats_(method: string, p: string, body: unknown): RouteResult {
    if (method === "GET" && p === "/v1/pats") {
      const list: PatList = { pats: this.pats, enforced: this.pats.length > 0 };
      return ok(list);
    }
    if (method === "POST" && p === "/v1/pats") {
      const b = body as { name?: string; scopes?: string[] } | null;
      if (!b?.name || !Array.isArray(b.scopes)) return err(400, "invalid_args", "name 和 scopes 必填");
      if (this.pats.some((x) => x.name === b.name)) return err(409, "conflict", "名字已存在");
      const info = this.issue(b.name, b.scopes);
      this.pats = [...this.pats, info];
      const secret: PatSecret = { pat: info, token: this.tokenOf(b.name)! };
      return ok(secret, 201);
    }
    const m = /^\/v1\/pats\/([^/]+)(\/rotate)?$/.exec(p);
    if (m) {
      const name = decodeURIComponent(m[1]!);
      const i = this.pats.findIndex((x) => x.name === name);
      if (i === -1) return err(404, "not_found", "没有这个 PAT");
      if (method === "POST" && m[2]) {
        const info = this.issue(name, this.pats[i]!.scopes);
        this.pats = this.pats.map((x, j) => (j === i ? info : x));
        const secret: PatSecret = { pat: info, token: this.tokenOf(name)! };
        return ok(secret);
      }
      if (method === "DELETE" && !m[2]) {
        fs.rmSync(this.pats[i]!.token_file, { force: true });
        this.pats = this.pats.filter((_, j) => j !== i);
        return { status: 204 };
      }
    }
    return err(404, "not_found", "没有这个接口");
  }

  /** The gateway's reach as the kernel reports it: the addresses follow from listen (which interfaces) and access (which sources). */
  gatewayInfo(): GatewayInfo {
    const { listen, access, hosts } = this.gateway;
    const port = listenPort(listen) ?? "8080";
    const url = (host: string): string => `http://${host}:${port}/mcp`;
    const addresses: GatewayAddress[] = [{ kind: "localhost", host: "127.0.0.1", url: url("127.0.0.1") }];
    if (listenScope(listen) === "all") {
      if (access.includes("lan")) addresses.push({ kind: "lan", host: FAKE_LAN_IP, url: url(FAKE_LAN_IP) });
      if (access.includes("tailnet")) {
        addresses.push({ kind: "tailnet_ip", host: FAKE_TAILNET_IP, url: url(FAKE_TAILNET_IP) });
        addresses.push({ kind: "tailnet_name", host: FAKE_TAILNET_NAME, url: url(FAKE_TAILNET_NAME) });
      }
      for (const h of hosts) addresses.push({ kind: "host", host: h, url: url(h) });
    }
    return { listen, url: addresses[0]!.url, auth: "pat", access: [...access], hosts: [...hosts], addresses };
  }

  /** PUT /v1/gateway: whole-replacement validation, then the new reach. Hot fields apply at once; the caller sets restart for listen. */
  putGateway(body: unknown): RouteResult {
    if (typeof body !== "object" || body === null || Array.isArray(body)) return err(400, "invalid_args", "请求体必须是对象");
    const b = body as Record<string, unknown>;
    const extra = Object.keys(b).find((k) => !["listen", "access", "hosts"].includes(k));
    if (extra !== undefined) return err(400, "invalid_args", `未知字段 ${extra}`, { field: extra });
    const diagnostics: ConfigDiagnostic[] = [];
    if (typeof b.listen !== "string" || !isListen(b.listen)) diagnostics.push({ pointer: "/gateway/listen", message: "监听地址只能是 :端口、127.0.0.1:端口 或 [::1]:端口 等形式" });
    const list = (value: unknown, pointer: string, valid: (s: string) => boolean, message: string): string[] => {
      if (!Array.isArray(value)) {
        diagnostics.push({ pointer, message: "必须是数组" });
        return [];
      }
      const out: string[] = [];
      value.forEach((x, i) => {
        if (typeof x !== "string" || !valid(x)) diagnostics.push({ pointer: `${pointer}/${i}`, message });
        else if (out.includes(x)) diagnostics.push({ pointer: `${pointer}/${i}`, message: "重复的条目" });
        else out.push(x);
      });
      return out;
    };
    const access = list(b.access, "/gateway/access", isAccessEntry, "只能是 localhost、lan、tailnet、单个 IP 或 CIDR");
    const hosts = list(b.hosts, "/gateway/hosts", isGatewayHost, "必须是域名，不能是 IP 地址");
    if (diagnostics.length > 0) return err(422, "config_invalid", "配置未通过校验", { diagnostics });
    const listen = b.listen as string;
    const restart = listen !== this.gateway.listen;
    this.gateway = { listen, access, hosts };
    const res: GatewayUpdateResponse = { listen, access, hosts, restart_required: restart };
    return ok(res);
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
    if (p === "/v1/pair/password") return this.pairPasswordRoute(method, body);
    if (method === "GET" && p === "/v1/pair/discovered") return ok({ devices: this.discovered });
    if (method === "POST" && p === "/v1/pair/connect") return this.connectPair(body);
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

  /**
   * The connection password. The peer password the fake accepts is PEER_PASSWORD; addresses ending in
   * ".limited" answer rate_limited and ".unreachable" answer unreachable, so tests can reach each error.
   */
  private pairPasswordRoute(method: string, body: unknown): RouteResult {
    if (method === "GET") return ok(this.pairPassword);
    if (method === "DELETE") {
      this.pairPassword = { set: false, updated_at: null, failed_attempts: 0, locked_until: null };
      return { status: 204 };
    }
    if (method === "PUT") {
      const pw = (body as { password?: unknown } | null)?.password;
      if (typeof pw !== "string" || [...pw].length < 10 || [...pw].length > 128 || pw.trim() !== pw) {
        return err(400, "invalid_args", "连接密码长度须为 10 到 128 个字符，首尾不能有空白", { field: "password" });
      }
      this.pairPassword = { set: true, updated_at: iso(), failed_attempts: 0, locked_until: null };
      return ok(this.pairPassword);
    }
    return err(404, "not_found", "没有这个接口");
  }

  private connectPair(body: unknown): RouteResult {
    const b = body as PairConnectRequest | null;
    if (!b || typeof b.password !== "string") return err(400, "invalid_args", "缺少 password", { field: "password" });
    if (b.address === undefined && b.fingerprint === undefined) return err(400, "invalid_args", "需要 address 或 fingerprint", { field: "address" });
    const device = b.fingerprint === undefined ? undefined : this.discovered.find((d) => d.fingerprint === b.fingerprint);
    const address = b.address ?? device?.address;
    if (address === undefined) return err(404, "not_found", "内网中没有发现这台设备");
    if (address.endsWith(".unreachable")) return err(422, "pair_rejected", "无法连接到对端", { reason: "unreachable" });
    if (address.endsWith(".limited")) return err(429, "rate_limited", "对端正在限制配对尝试，请稍后再试", { retry_after_ms: 30000 });
    if (b.password !== PEER_PASSWORD) return err(422, "pair_rejected", "连接密码不正确，或对端不是待配对的 xmesh 设备", { reason: "wrong_password" });
    const name = device?.name ?? "peer-typed";
    this.discovered = this.discovered.filter((d) => d.name !== name);
    this.imported.push({ name, tags: ["tag:peer", "tag:device"] });
    return ok({ name, fingerprint: device?.fingerprint ?? "3f2a9c4e1b7d8a60f5e2c9b1d4a7e8f03c6b5a29d1e4f7a8b0c3d6e9f2a5b8c1", address, tags: ["tag:peer", "tag:device"] });
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

/** The connection password the fake's peers accept. */
export const PEER_PASSWORD = "correct horse battery staple";

type Node = Record<string, unknown>;

const isNode = (v: unknown): v is Node => typeof v === "object" && v !== null && !Array.isArray(v);

/** The value at a pointer, or undefined. Only maps are walked. */
function pointerGet(doc: Node, segments: string[]): unknown {
  let node: unknown = doc;
  for (const seg of segments) {
    if (!isNode(node)) return undefined;
    node = node[seg];
  }
  return node;
}

/** Sets the value at a pointer. Missing maps are created; a scalar or an array on the way is a conflict (false). */
function pointerSet(doc: Node, segments: string[], value: unknown): boolean {
  let node: Node = doc;
  for (const seg of segments.slice(0, -1)) {
    if (node[seg] === undefined) node[seg] = {};
    const next = node[seg];
    if (!isNode(next)) return false;
    node = next;
  }
  node[segments[segments.length - 1]!] = structuredClone(value);
  return true;
}

/** Deletes the key at a pointer. A missing path is a no-op here; a scalar on the way is a conflict (false). */
function pointerRemove(doc: Node, segments: string[]): boolean {
  let node: Node = doc;
  for (const seg of segments.slice(0, -1)) {
    const next = node[seg];
    if (next === undefined) return true;
    if (!isNode(next)) return false;
    node = next;
  }
  delete node[segments[segments.length - 1]!];
  return true;
}

/** The YAML without comments and blank lines: what the fake treats as the configuration itself. */
function semantic(content: string): string {
  return content
    .split("\n")
    .filter((line) => line.trim() !== "" && !line.trim().startsWith("#"))
    .join("\n");
}
