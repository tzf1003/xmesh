// The panel's data operations. Each method has a fixed Control API route (the
// renderer never supplies a path or a method), takes arguments the IPC layer
// already validated, and turns every outcome into a plain result object so no
// exception or stack crosses into the page.

import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";

import { ControlRequestError } from "../shared/control";
import type {
  AuditQuery,
  AuditResponse,
  CallEventData,
  ConfigPutResponse,
  ConfigResponse,
  DrainMode,
  IngressClientsResponse,
  IngressPasswordResponse,
  PairExportResponse,
  PairImportResponse,
  PatList,
  PatSecret,
  SessionsResponse,
} from "../shared/control";
import {
  DENY_FILE_MAX_BYTES,
  type ActionError,
  type ActionResult,
  type ConfigReloadResponse,
  type ConfigSaveRequest,
  type DataResult,
  type DenyFileCreateRequest,
  type DenyFileCreated,
  type DenyFileResponse,
  type DenyFileSaveRequest,
  type DenyFileSaved,
  type PairImportArgs,
  type PatCreateArgs,
} from "../shared/ipc";
import { DENY_FILE_NAME } from "../shared/setup";
import type { ControlClient } from "./client";
import type { ShellController } from "./controller";

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

function toError(e: unknown): ActionError {
  if (e instanceof ControlRequestError) {
    const out: ActionError = { code: e.code, message: e.message };
    if (e.reason !== undefined) out.reason = e.reason;
    if (e.details.field !== undefined) out.field = e.details.field;
    if (e.details.current_etag !== undefined) out.current_etag = e.details.current_etag;
    if (e.details.diagnostics !== undefined) out.diagnostics = e.details.diagnostics;
    return out;
  }
  // Socket errors and timeouts: the kernel is gone or stuck. The text is Node's, never the kernel's.
  return { code: "unavailable", message: e instanceof Error ? e.message : String(e) };
}

export class PanelService {
  constructor(private readonly controller: CapabilityClient) {}

  private async data<T>(capability: Parameters<CapabilityClient["requireCapable"]>[0], run: (c: ControlClient) => Promise<unknown>): Promise<DataResult<T>> {
    try {
      const c = this.controller.requireCapable(capability);
      return { ok: true, data: (await run(c)) as T };
    } catch (e) {
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
    return this.data("sessions", (c) => c.call("GET", "/v1/sessions"));
  }

  getConfig(): Promise<DataResult<ConfigResponse>> {
    return this.data("config.read", (c) => c.call("GET", "/v1/config"));
  }

  putConfig(req: ConfigSaveRequest): Promise<DataResult<ConfigPutResponse>> {
    return this.data("config.write", (c) =>
      c.call("PUT", "/v1/config", { body: { content: req.content, dry_run: req.dryRun }, ifMatch: req.etag }),
    );
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
      const tmp = `${file}.${process.pid}.tmp`;
      fs.writeFileSync(tmp, req.text, { mode: 0o600 });
      fs.renameSync(tmp, file);
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
    return this.data("config.write", (c) => c.call("POST", "/v1/config/reload"));
  }

  listPats(): Promise<DataResult<PatList>> {
    return this.data("pats", (c) => c.call("GET", "/v1/pats"));
  }

  createPat(args: PatCreateArgs): Promise<DataResult<PatSecret>> {
    return this.data("pats", (c) => c.call("POST", "/v1/pats", { body: { name: args.name, scopes: args.scopes } }));
  }

  /** `name` is a validated node name, so it is safe as a path segment. */
  rotatePat(name: string): Promise<DataResult<PatSecret>> {
    return this.data("pats", (c) => c.call("POST", `/v1/pats/${encodeURIComponent(name)}/rotate`));
  }

  deletePat(name: string): Promise<ActionResult> {
    return this.action("pats", (c) => c.call("DELETE", `/v1/pats/${encodeURIComponent(name)}`));
  }

  listIngressClients(): Promise<DataResult<IngressClientsResponse>> {
    return this.data("ingress", (c) => c.call("GET", "/v1/ingress/clients"));
  }

  revokeIngressClient(clientId: string): Promise<ActionResult> {
    return this.action("ingress", (c) => c.call("DELETE", `/v1/ingress/clients/${encodeURIComponent(clientId)}`));
  }

  rotateIngressPassword(password: string): Promise<DataResult<IngressPasswordResponse>> {
    return this.data("ingress", (c) => c.call("PUT", "/v1/ingress/password", { body: { password } }));
  }

  exportPair(): Promise<DataResult<PairExportResponse>> {
    return this.data("pair", (c) => c.call("POST", "/v1/pair/export"));
  }

  importPair(args: PairImportArgs): Promise<DataResult<PairImportResponse>> {
    const body: Record<string, unknown> = { pair: args.pair, fingerprint: args.fingerprint };
    if (args.tags !== undefined) body.tags = args.tags;
    return this.data("pair", (c) => c.call("POST", "/v1/pair/import", { body }));
  }

  queryAudit(q: AuditQuery): Promise<DataResult<AuditResponse>> {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (typeof v === "string") params.set(k, v);
    const qs = params.toString();
    return this.data("audit", (c) => c.call("GET", qs === "" ? "/v1/audit" : `/v1/audit?${qs}`));
  }

  recentCalls(): CallEventData[] {
    return this.controller.recentCalls;
  }
}
