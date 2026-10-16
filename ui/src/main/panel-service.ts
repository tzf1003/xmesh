// The panel's data operations. Each method has a fixed Control API route (the
// renderer never supplies a path or a method), takes arguments the IPC layer
// already validated, and turns every outcome into a plain result object so no
// exception or stack crosses into the page.

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
import type {
  ActionError,
  ActionResult,
  ConfigSaveRequest,
  DataResult,
  PairImportArgs,
  PatCreateArgs,
} from "../shared/ipc";
import type { ControlClient } from "./client";
import type { ShellController } from "./controller";

export type CapabilityClient = Pick<ShellController, "requireCapable" | "recentCalls" | "drain">;

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
