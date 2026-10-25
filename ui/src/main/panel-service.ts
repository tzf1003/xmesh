// The panel's data operations. Each method has a fixed Control API route (the
// renderer never supplies a path or a method), takes arguments the IPC layer
// already validated, and turns every outcome into a plain result object so no
// exception or stack crosses into the page.

import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import { configYaml, parseDocument } from "./yaml";

import { ControlRequestError } from "../shared/control";
import type { CiRequest, CiResultFor } from "../shared/ci";
import type { ComputerRequest, ComputerResult } from "../shared/computer";
import type {
  AuditQuery,
  AuditResponse,
  BackendEnvResponse,
  CallEventData,
  ConfigPutResponse,
  ConfigResponse,
  DrainMode,
  GatewayUpdateRequest,
  GatewayUpdateResponse,
  IngressClientsResponse,
  IngressPasswordResponse,
  PairConnectResponse,
  PairDiscoveredResponse,
  PairExportResponse,
  PairImportResponse,
  OfferAcceptResponse,
  OffersResponse,
  PairTrustUpdateResponse,
  PairPasswordState,
  PatList,
  PatSecret,
  SessionsResponse,
} from "../shared/control";
import {
  CONFIG_MAX_BYTES,
  DENY_FILE_MAX_BYTES,
  type ActionError,
  type ActionResult,
  type BackendEnvRequest,
  type ConfigPatchRequest,
  type ConfigReloadResponse,
  type ConfigSaveRequest,
  type ConfigSaveResponse,
  type DataResult,
  type DenyFileCreateRequest,
  type DenyFileCreated,
  type DenyFileResponse,
  type DenyFileSaveRequest,
  type DenyFileSaved,
  type GatewayPatRotated,
  type PairConnectArgs,
  type PairImportArgs,
  type PatCreateArgs,
} from "../shared/ipc";
import { DENY_FILE_NAME, isAuthKey } from "../shared/setup";
import type { ControlClient } from "./client";
import type { ShellController } from "./controller";
import { resumeComputerHost, setupComputerHost } from "./computer-host";

export type CapabilityClient = Pick<ShellController, "requireCapable" | "recentCalls" | "drain" | "snapshot">;

/** `agent.cwd_policy.deny_file` when the config names an absolute path for it. */
export function denyFileOf(parsed: unknown): string | undefined {
  const agent = (parsed as { agent?: { cwd_policy?: { deny_file?: unknown } } } | null)?.agent;
  const file = agent?.cwd_policy?.deny_file;
  return typeof file === "string" && path.isAbsolute(file) ? file : undefined;
}

/** The file's text, or undefined when it does not exist. Other read errors propagate. */
function readOptional(file: string): string | undefined {
  try {
    return fs.readFileSync(file, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw e;
  }
}

function etagOf(text: string): string {
  return text === "" ? "" : `sha256-${crypto.createHash("sha256").update(text).digest("hex")}`;
}

/** The PAT that is the gateway's own token: created by the kernel, rotated from the panel. */
export const GATEWAY_PAT = "default";

function toError(e: unknown): ActionError {
  if (e instanceof ControlRequestError) {
    const out: ActionError = { code: e.code, message: e.message };
    if (e.reason !== undefined) out.reason = e.reason;
    if (e.details.field !== undefined) out.field = e.details.field;
    if (e.details.current_etag !== undefined) out.current_etag = e.details.current_etag;
    if (e.details.retry_after_ms !== undefined) out.retry_after_ms = e.details.retry_after_ms;
    if (e.details.diagnostics !== undefined) out.diagnostics = e.details.diagnostics;
    return out;
  }
  // Socket errors and timeouts: the kernel is gone or stuck. The text is Node's, never the kernel's.
  return { code: "unavailable", message: e instanceof Error ? e.message : String(e) };
}

export class PanelService {
  constructor(private readonly controller: CapabilityClient,
    private readonly setupDesktop: (endpoint?: string) => Promise<void> = endpoint => setupComputerHost(undefined, endpoint),
    private readonly resumeDesktop: (endpoint?: string) => Promise<void> = endpoint => resumeComputerHost(undefined, endpoint)) {}

  private async data<T>(capability: Parameters<CapabilityClient["requireCapable"]>[0], run: (c: ControlClient) => Promise<unknown>): Promise<DataResult<T>> {
    try {
      const c = this.controller.requireCapable(capability);
      return { ok: true, data: (await run(c)) as T };
    } catch (e) {
      if (process.env.XMESH_UI_TEST === "1" && process.env.XMESH_UI_BACKGROUND_TEST === "1") console.error("test-control-error", JSON.stringify(toError(e)));
      return { ok: false, error: toError(e) };
    }
  }

  private async action(capability: Parameters<CapabilityClient["requireCapable"]>[0], run: (c: ControlClient) => Promise<unknown>): Promise<ActionResult> {
    try {
      await run(this.controller.requireCapable(capability));
      return { ok: true };
    } catch (e) {
      return { ok: false, error: toError(e) };
    }
  }

  async drain(mode: DrainMode): Promise<ActionResult> {
    try {
      await this.controller.drain(mode);
      return { ok: true };
    } catch (e) {
      return { ok: false, error: toError(e) };
    }
  }

  getSessions(): Promise<DataResult<SessionsResponse>> {
    return this.data("sessions", (c) => c.call("GET", "/v2/sessions"));
  }

  getConfig(): Promise<DataResult<ConfigResponse>> {
    return this.data("config.read", (c) => c.call("GET", "/v2/config"));
  }

  putConfig(req: ConfigSaveRequest): Promise<DataResult<ConfigSaveResponse>> {
    return this.data("config.write", async (c) => {
      const put = (content: string, dryRun: boolean, authkey?: string): Promise<unknown> => {
        const body = { content, dry_run: dryRun, ...(authkey !== undefined && { authkey }) };
        if (Buffer.byteLength(JSON.stringify(body)) > CONFIG_MAX_BYTES) {
          throw new ControlRequestError("payload_too_large", "配置正文与 auth key 编码后超过 1 MiB，请缩小配置后重试");
        }
        return c.call("PUT", "/v2/config", { body, ifMatch: req.etag });
      };
      if (req.authkey === undefined) return put(req.content, req.dryRun);
      const authkey = req.authkey.trim();
      if (!isAuthKey(authkey)) throw new ControlRequestError("invalid_args", "请输入有效的 auth key");
      const connection = this.controller.snapshot.connection;
      const stateDir = connection.kind === "connected" ? connection.status.paths.state_dir : undefined;
      if (typeof stateDir !== "string" || !path.isAbsolute(stateDir)) {
        throw new ControlRequestError("unavailable", "内核没有报告状态目录的位置");
      }
      const doc = parseDocument(req.content);
      if (doc.errors.length > 0 || doc.get("schema_version") !== 2 || doc.getIn(["mesh", "tailnet", "enabled"]) !== true) {
        throw new ControlRequestError("invalid_args", "auth key 需要启用 Tailnet");
      }
      const directory = path.join(stateDir, "authkeys");
      // The preview chooses a fresh location. Confirming uses that same location,
      // so the reviewed config is exact and an active credential is never overwritten.
      const file = req.dryRun ? path.join(directory, `authkey-${crypto.randomUUID()}`) : doc.getIn(["mesh", "tailnet", "authkey_file"]);
      if (typeof file !== "string" || path.dirname(file) !== directory || !/^authkey-[0-9a-f-]{36}$/.test(path.basename(file))) {
        throw new ControlRequestError("invalid_args", "请先预览 auth key 设置再保存");
      }
      doc.setIn(["mesh", "tailnet", "authkey_file"], file);
      doc.deleteIn(["mesh", "tailnet", "auth_method"]);
      const content = configYaml(doc);
      // The kernel owns credential creation and config commit, including system
      // services whose private state directory the desktop cannot write to.
      const saved = await put(content, req.dryRun, authkey) as ConfigPutResponse;
      return { ...saved, content };
    });
  }

  /** `PATCH /v2/config` through the generic client call: If-Match from the etag, the same error mapping as PUT. */
  patchConfig(req: ConfigPatchRequest): Promise<DataResult<ConfigPutResponse>> {
    return this.data("config.write", (c) =>
      c.call("PATCH", "/v2/config", { body: { edits: req.edits, dry_run: req.dryRun }, ifMatch: req.etag }),
    );
  }

  /**
   * Variables of one stdio backend. `name` is a validated backend name, so it is safe as a path segment. The values
   * travel in the request body only: they are not kept, logged or returned, and the kernel answers with names.
   */
  patchBackendEnv(req: BackendEnvRequest): Promise<DataResult<BackendEnvResponse>> {
    const body: { set?: Record<string, string>; unset?: string[] } = {};
    if (Object.keys(req.set).length > 0) body.set = req.set;
    if (req.unset.length > 0) body.unset = req.unset;
    return this.data("config.write", (c) => c.call("PATCH", `/v2/backends/${encodeURIComponent(req.name)}/env`, { body }));
  }

  clearBackendEnv(name: string): Promise<ActionResult> {
    return this.action("config.write", (c) => c.call("DELETE", `/v2/backends/${encodeURIComponent(name)}/env`));
  }

  /** The deny list file the config names. The path comes from the config, so the renderer cannot point this at another file. */
  async readDenyFile(): Promise<DataResult<DenyFileResponse>> {
    const cfg = await this.getConfig();
    if (!cfg.ok) return cfg;
    const file = denyFileOf(cfg.data.parsed);
    if (file === undefined) return { ok: true, data: { exists: false, text: "", etag: "" } };
    try {
      const text = readOptional(file);
      return { ok: true, data: { path: file, exists: text !== undefined, text: text ?? "", etag: etagOf(text ?? "") } };
    } catch (e) {
      return { ok: false, error: { code: "io", message: e instanceof Error ? e.message : String(e) } };
    }
  }

  /** Writes the file only if it still holds what the edit started from; the kernel reloads it itself. */
  async writeDenyFile(req: DenyFileSaveRequest): Promise<DataResult<DenyFileSaved>> {
    if (Buffer.byteLength(req.text) > DENY_FILE_MAX_BYTES) return { ok: false, error: { code: "payload_too_large", message: "禁止目录文件过大" } };
    const cfg = await this.getConfig();
    if (!cfg.ok) return cfg;
    const file = denyFileOf(cfg.data.parsed);
    if (file === undefined) return { ok: false, error: { code: "not_configured", message: "配置里没有 deny_file" } };
    try {
      const current = readOptional(file) ?? "";
      if (etagOf(current) !== req.etag) {
        return { ok: false, error: { code: "precondition_failed", message: "禁止目录文件已被修改，请重新读取后再保存", current_etag: etagOf(current) } };
      }
      const dir = fs.mkdtempSync(path.join(path.dirname(file), ".xmesh-deny-"));
      try {
        const tmp = path.join(dir, "deny.yaml");
        fs.writeFileSync(tmp, req.text, { mode: 0o600, flag: "wx" });
        fs.renameSync(tmp, file);
      } finally {
        fs.rmSync(dir, { recursive: true, force: true });
      }
      return { ok: true, data: { etag: etagOf(req.text) } };
    } catch (e) {
      return { ok: false, error: { code: "io", message: e instanceof Error ? e.message : String(e) } };
    }
  }

  /**
   * Creates the deny list file for a config that names none, beside the config file the kernel reports. A file
   * already there with the same text is kept; one with other text is never replaced.
   */
  async createDenyFile(req: DenyFileCreateRequest): Promise<DataResult<DenyFileCreated>> {
    if (Buffer.byteLength(req.text) > DENY_FILE_MAX_BYTES) return { ok: false, error: { code: "payload_too_large", message: "禁止目录文件过大" } };
    const cfg = await this.getConfig();
    if (!cfg.ok) return cfg;
    if (denyFileOf(cfg.data.parsed) !== undefined) return { ok: false, error: { code: "already_configured", message: "配置里已经有 deny_file" } };
    const node = this.controller.snapshot.connection;
    const configFile = node.kind === "connected" ? node.status.paths?.config_file : undefined;
    if (typeof configFile !== "string" || configFile === "") return { ok: false, error: { code: "unavailable", message: "内核没有报告配置文件的位置" } };
    const file = path.join(path.dirname(configFile), DENY_FILE_NAME);
    try {
      fs.writeFileSync(file, req.text, { flag: "wx", mode: 0o600 });
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "EEXIST") return { ok: false, error: { code: "io", message: e instanceof Error ? e.message : String(e) } };
      if (readOptional(file) !== req.text) {
        return { ok: false, error: { code: "exists", message: "这个节点目录里已有禁止目录文件，不会覆盖。请到「设置」页编辑它。" } };
      }
    }
    return { ok: true, data: { path: file, etag: etagOf(req.text) } };
  }

  /** Has the kernel reload its config in place. A kernel without the route answers with an error, and the page restarts instead. */
  reloadConfig(): Promise<DataResult<ConfigReloadResponse>> {
    return this.data("config.write", (c) => c.call("POST", "/v2/config/reload"));
  }

  listPats(): Promise<DataResult<PatList>> {
    return this.data("pats", (c) => c.call("GET", "/v2/pats"));
  }

  createPat(args: PatCreateArgs): Promise<DataResult<PatSecret>> {
    return this.data("pats", (c) => c.call("POST", "/v2/pats", { body: { name: args.name, scopes: args.scopes } }));
  }

  /** `name` is a validated node name, so it is safe as a path segment. */
  rotatePat(name: string): Promise<DataResult<PatSecret>> {
    return this.data("pats", (c) => c.call("POST", `/v2/pats/${encodeURIComponent(name)}/rotate`));
  }

  /**
   * PUT /v2/gateway: the whole reach replacement. Access and hosts apply at once; a changed listen
   * needs a restart, which the response's `restart_required` says.
   */
  updateGateway(req: GatewayUpdateRequest): Promise<DataResult<GatewayUpdateResponse>> {
    return this.data("gateway", (c) => c.call("PUT", "/v2/gateway", { body: req }));
  }

  /**
   * Rotates the gateway PAT. The kernel answers with the new token too; it is dropped here, so only the
   * PAT's name and modification time reach the page. The token is in the token file, which is all anyone needs.
   */
  rotateGatewayPat(): Promise<DataResult<GatewayPatRotated>> {
    return this.data("pats", async (c) => {
      const list = (await c.call("GET", "/v2/pats")) as PatList;
      if (!list.pats.some((p) => p.name === GATEWAY_PAT)) throw new ControlRequestError("not_found", "the kernel has no default PAT");
      const secret = (await c.call("POST", `/v2/pats/${encodeURIComponent(GATEWAY_PAT)}/rotate`)) as PatSecret;
      return { name: secret.pat.name, modified_at: secret.pat.modified_at };
    });
  }

  deletePat(name: string): Promise<ActionResult> {
    return this.action("pats", (c) => c.call("DELETE", `/v2/pats/${encodeURIComponent(name)}`));
  }

  listIngressClients(): Promise<DataResult<IngressClientsResponse>> {
    return this.data("ingress", (c) => c.call("GET", "/v2/ingress/clients"));
  }

  revokeIngressClient(clientId: string): Promise<ActionResult> {
    return this.action("ingress", (c) => c.call("DELETE", `/v2/ingress/clients/${encodeURIComponent(clientId)}`));
  }

  rotateIngressPassword(password: string): Promise<DataResult<IngressPasswordResponse>> {
    return this.data("ingress", (c) => c.call("PUT", "/v2/ingress/password", { body: { password } }));
  }

  exportPair(): Promise<DataResult<PairExportResponse>> {
    return this.data("pair", (c) => c.call("POST", "/v2/pair/export"));
  }

  importPair(args: PairImportArgs): Promise<DataResult<PairImportResponse>> {
    const body: Record<string, unknown> = { pair: args.pair, fingerprint: args.fingerprint };
    if (args.tags !== undefined) body.tags = args.tags;
    return this.data("pair", (c) => c.call("POST", "/v2/pair/import", { body }));
  }

  getPairPassword(): Promise<DataResult<PairPasswordState>> {
    return this.data("pair", (c) => c.call("GET", "/v2/pair/password"));
  }

  setPairPassword(password: string): Promise<DataResult<PairPasswordState>> {
    return this.data("pair", (c) => c.call("PUT", "/v2/pair/password", { body: { password } }));
  }

  revokeDevice(deviceId: string): Promise<ActionResult> {
    return this.action("pair", (c) => c.call("DELETE", `/v2/pair/trust/${deviceId}`));
  }

  setDeviceTags(deviceId: string, tags: string[]): Promise<DataResult<PairTrustUpdateResponse>> {
    return this.data("pair", (c) => c.call("PATCH", `/v2/pair/trust/${deviceId}`, { body: { tags } }));
  }

  listOffers(): Promise<DataResult<OffersResponse>> {
    return this.data("pair", (c) => c.call("GET", "/v2/offers"));
  }

  acceptOffer(offerId: string): Promise<DataResult<OfferAcceptResponse>> {
    return this.data("pair", (c) => c.call("POST", `/v2/offers/${offerId}/accept`));
  }

  rejectOffer(offerId: string): Promise<ActionResult> {
    return this.action("pair", (c) => c.call("DELETE", `/v2/offers/${offerId}`));
  }

  clearPairPassword(): Promise<ActionResult> {
    return this.action("pair", (c) => c.call("DELETE", "/v2/pair/password"));
  }

  discoverPairs(): Promise<DataResult<PairDiscoveredResponse>> {
    return this.data("pair", (c) => c.call("GET", "/v2/pair/discovered"));
  }

  connectPair(args: PairConnectArgs): Promise<DataResult<PairConnectResponse>> {
    const body: Record<string, unknown> = { password: args.password };
    if (args.path !== undefined) body.path = args.path;
    if (args.address !== undefined) body.address = args.address;
    if (args.fingerprint !== undefined) body.fingerprint = args.fingerprint;
    return this.data("pair", (c) => c.call("POST", "/v2/pair/connect", { body }));
  }

  /**
   * One GitHub Actions runner request (CTR-CTL-15). The route is chosen here from the validated request; the
   * renderer never names a path. Tokens are in `req` and go straight to the kernel; no result carries one.
   */
  ci<R extends CiRequest>(req: R): Promise<DataResult<CiResultFor<R>>> {
    const id = (x: string): string => encodeURIComponent(x);
    const run = (call: (c: ControlClient) => Promise<unknown>): Promise<DataResult<CiResultFor<R>>> =>
      this.data("ci", async (c) => (await call(c)) ?? null);
    const r = req as CiRequest;
    switch (r.op) {
      case "get":
        return run((c) => c.call("GET", "/v2/ci"));
      case "account.create": {
        const body: Record<string, unknown> = { token: r.token };
        if (r.host !== undefined) body.host = r.host;
        return run((c) => c.call("POST", "/v2/ci/accounts", { body }));
      }
      case "account.delete":
        return run((c) => c.call("DELETE", `/v2/ci/accounts/${id(r.id)}`));
      case "repos": {
        const params = new URLSearchParams();
        if (r.q !== undefined && r.q !== "") params.set("q", r.q);
        if (r.page !== undefined) params.set("page", r.page);
        const qs = params.toString();
        return run((c) => c.call("GET", `/v2/ci/accounts/${id(r.id)}/repos${qs === "" ? "" : `?${qs}`}`));
      }
      case "binding.create":
        return run((c) => c.call("POST", "/v2/ci/bindings", { body: r.args }));
      case "binding.patch":
        return run((c) => c.call("PATCH", `/v2/ci/bindings/${id(r.id)}`, { body: r.patch }));
      case "binding.delete":
        return run((c) => c.call("DELETE", `/v2/ci/bindings/${id(r.id)}${r.force === true ? "?force=true" : ""}`));
      case "binding.restart":
        return run((c) => c.call("POST", `/v2/ci/bindings/${id(r.id)}/restart`));
      case "binding.repair": {
        const body = r.registration_token === undefined ? undefined : { registration_token: r.registration_token };
        return run((c) => c.call("POST", `/v2/ci/bindings/${id(r.id)}/repair`, body === undefined ? {} : { body }));
      }
      case "import.inspect":
        return run((c) => c.call("POST", "/v2/ci/import/inspect", { body: { dir: r.dir } }));
    }
  }

  computer(request: ComputerRequest): Promise<DataResult<ComputerResult>> {
    return this.data("computer", async (client) => {
      const connection = this.controller.snapshot.connection;
      const endpoint = connection.kind === "connected" ? connection.status.paths.computer_endpoint : undefined;
      switch (request.op) {
        case "get": return client.call("GET", "/v2/computer");
        case "setup": await this.setupDesktop(endpoint); return client.call("GET", "/v2/computer");
        case "pause": {
          // Agent pause is independent of Host availability. A suspended Host
          // continues enforcing its own local guard if its resume fails.
          if (!request.paused) await this.resumeDesktop(endpoint).catch(() => undefined);
          return client.call("POST", "/v2/computer/pause", { body: { paused: request.paused } });
        }
        case "revoke": return client.call("POST", "/v2/computer/revoke", { body: { session_id: request.session_id } });
      }
    });
  }

  queryAudit(q: AuditQuery): Promise<DataResult<AuditResponse>> {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (typeof v === "string") params.set(k, v);
    const qs = params.toString();
    return this.data("audit", (c) => c.call("GET", qs === "" ? "/v2/audit" : `/v2/audit?${qs}`));
  }

  recentCalls(): CallEventData[] {
    return this.controller.recentCalls;
  }
}
