// Control API v1 as the shell consumes it (docs/contracts/control.md, CTR-CTL-*).
//
// The wire types are generated from the contract schemas and live in
// ./generated/control.ts (copied by `npm run sync-types`, checked in CI).
// This file adds what the generated file cannot express: version handling,
// light validation of what arrives from the socket, and the error classes.

import {
  CONTROL_API_VERSION,
  CONTROL_CAPABILITIES,
  CONTROL_EVENT_TYPES,
  type Capability,
  type ControlEndpointFile,
  type ControlError,
  type ControlEvent,
  type Peer,
  type StatusResponse,
  type Warning,
} from "./generated/control";

export * from "./generated/control";

/** Thrown for a kernel answer that is an error envelope (`ControlError` in the contract). */
export class ControlRequestError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly httpStatus?: number,
    readonly reason?: string,
    /** Remaining fields of the error body the panel shows (`field`, `current_etag`, `diagnostics`). */
    readonly details: Pick<ControlError, "field" | "current_etag" | "diagnostics" | "retry_after_ms"> = {},
  ) {
    super(message);
    this.name = "ControlRequestError";
  }
}

/** Kernel and UI disagree on the Control API major version. */
export class VersionMismatchError extends Error {
  constructor(
    readonly apiVersion: string,
    readonly kernelVersion: string,
  ) {
    super(`control api ${apiVersion || "(none)"} is not supported (want ${CONTROL_API_VERSION})`);
    this.name = "VersionMismatchError";
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Returns the major of "1", "1.2" or "v1.2.3"; NaN when unparsable. */
export function apiMajor(version: string): number {
  const m = /^v?(\d+)(?:\.|$)/.exec(version.trim());
  return m ? Number(m[1]) : Number.NaN;
}

export function isSupportedApiVersion(version: string): boolean {
  return apiMajor(version) === apiMajor(CONTROL_API_VERSION);
}

const KNOWN_CAPABILITIES: readonly string[] = CONTROL_CAPABILITIES;
const KNOWN_EVENTS: readonly string[] = CONTROL_EVENT_TYPES;

/** A capability the kernel advertises that this UI knows about. Unknown ones are ignored (minor versions add them). */
export function hasCapability(capabilities: readonly string[], c: Capability): boolean {
  return capabilities.includes(c);
}

/** `control.json`. Returns undefined when it is not usable (half-written, wrong shape). */
export function parseManifest(raw: unknown): ControlEndpointFile | undefined {
  if (!isRecord(raw)) return undefined;
  const { endpoint, transport, api_version, kernel_version, node, boot_id, started_at } = raw;
  if (typeof endpoint !== "string" || endpoint === "") return undefined;
  if (transport !== "unix" && transport !== "pipe") return undefined;
  if (endpoint.includes("\0")) return undefined;
  if (transport === "unix" ? !/^\/.+/.test(endpoint) : !/^\\\\\.\\pipe\\[^\\/]+$/.test(endpoint)) return undefined;
  if (typeof api_version !== "string") return undefined;
  const caps = Array.isArray(raw.capabilities)
    ? (raw.capabilities.filter((c): c is Capability => typeof c === "string" && KNOWN_CAPABILITIES.includes(c)) as Capability[])
    : [];
  return {
    api_version,
    kernel_version: typeof kernel_version === "string" ? kernel_version : "",
    node: typeof node === "string" ? node : "",
    pid: typeof raw.pid === "number" ? raw.pid : 0,
    boot_id: typeof boot_id === "string" ? boot_id : "",
    transport,
    endpoint,
    started_at: typeof started_at === "string" ? started_at : "",
    capabilities: caps,
  };
}

/**
 * Accepts a `StatusResponse`. The schema is closed on the kernel side and
 * clients ignore unknown fields, so only what the shell reads is checked.
 */
export function parseStatus(raw: unknown): StatusResponse {
  const strings = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === "string");
  const fields = (v: Record<string, unknown>, names: string[]): boolean => names.every((k) => typeof v[k] === "string");
  const counts = (v: Record<string, unknown>, names: string[]): boolean => names.every((k) => typeof v[k] === "number" && Number.isFinite(v[k]) && v[k] >= 0);
  if (
    !isRecord(raw) ||
    !isRecord(raw.control) ||
    !isRecord(raw.drain) ||
    !isRecord(raw.paths) ||
    !isRecord(raw.limits) ||
    !isRecord(raw.peers) ||
    !isRecord(raw.network) ||
    !fields(raw.control, ["api_version", "kernel_version", "boot_id"]) ||
    (raw.control.capabilities !== undefined && !strings(raw.control.capabilities)) ||
    !fields(raw.network, ["node_id"]) ||
    typeof raw.network.mode !== "string" ||
    !strings(raw.network.tags) ||
    typeof raw.drain.draining !== "boolean" ||
    (raw.drain.mode !== null && typeof raw.drain.mode !== "string") ||
    (raw.drain.since !== null && typeof raw.drain.since !== "string") ||
    !fields(raw.paths, ["config_file", "state_dir"]) ||
    ![raw.paths.log_dir, raw.paths.audit_dir].every((p) => p === null || typeof p === "string") ||
    !counts(raw.limits, ["exec_shared", "exec_exclusive", "exec_shared_in_use", "exec_exclusive_in_use", "pty_max_sessions", "pty_max_per_caller", "pty_in_use", "pty_active", "pty_closed", "pty_closed_retain_max"]) ||
    !counts(raw.peers, ["total", "online"]) ||
    !fields(raw, ["hostname", "home", "arch", "started_at"]) ||
    typeof raw.os !== "string" ||
    typeof raw.lifecycle !== "string" ||
    !["/", "\\"].includes(raw.path_sep as string) ||
    typeof raw.restart_required !== "boolean" ||
    typeof raw.authkey_readable !== "boolean" ||
    !counts(raw, ["pid", "policy_reload_failures"]) ||
    !strings(raw.shells) ||
    !Array.isArray(raw.workspaces) ||
    !raw.workspaces.every((v) => isRecord(v) && fields(v, ["alias", "path", "desc"]) && (v.error === undefined || typeof v.error === "string")) ||
    !Array.isArray(raw.backends) ||
    !raw.backends.every((v) => isRecord(v) && fields(v, ["name", "policy", "state"]) && typeof v.favorite === "boolean" && (v.env_names === undefined || strings(v.env_names))) ||
    !Array.isArray(raw.warnings) ||
    !strings(raw.roles)
  ) {
    throw new ControlRequestError("internal", "unexpected status response");
  }
  const status = raw as unknown as StatusResponse;
  // Tolerate a `control.capabilities` that is missing: treat as none.
  if (!Array.isArray(status.control.capabilities)) status.control.capabilities = [];
  // Kernels before CTR-CTL-13 send no reach fields: the list is then just the localhost address.
  if (status.gateway !== undefined) {
    const gw = raw.gateway;
    if (!isRecord(gw) || !fields(gw, ["listen", "url", "auth"]) ||
      (gw.access !== undefined && !strings(gw.access)) ||
      (gw.hosts !== undefined && !strings(gw.hosts)) ||
      (gw.addresses !== undefined && (!Array.isArray(gw.addresses) || !gw.addresses.every((v) => isRecord(v) && fields(v, ["kind", "host", "url"]))))) {
      throw new ControlRequestError("internal", "unexpected status response");
    }
    status.gateway.access ??= [];
    status.gateway.hosts ??= [];
    status.gateway.addresses ??= [];
  }
  if (raw.ingress !== undefined && (!isRecord(raw.ingress) || typeof raw.ingress.listen !== "string" || (raw.ingress.issuer !== null && typeof raw.ingress.issuer !== "string"))) {
    throw new ControlRequestError("internal", "unexpected status response");
  }
  if (raw.ci !== undefined && (!isRecord(raw.ci) || !Array.isArray(raw.ci.bindings) || !raw.ci.bindings.every((b) =>
    isRecord(b) && fields(b, ["id", "target", "scope", "source"]) && strings(b.labels) && counts(b, ["concurrency"]) &&
    Array.isArray(b.instances) && b.instances.every((i) => isRecord(i) && fields(i, ["name", "state", "since"]) && counts(i, ["index", "restarts"]))))) {
    throw new ControlRequestError("internal", "unexpected status response");
  }
  status.warnings = status.warnings.filter(
    (w): w is Warning => isRecord(w) && typeof w.code === "string" && typeof w.message === "string",
  );
  return status;
}

export function parsePeers(raw: unknown): Peer[] {
  const list = isRecord(raw) ? raw.peers : undefined;
  if (!Array.isArray(list)) return [];
  return list.filter((p): p is Peer => isRecord(p) && typeof p.name === "string" && typeof p.online === "boolean");
}

/** Parses the `data:` line of an SSE event. Unknown event types are dropped (minor versions add them). */
export function parseEvent(data: string): ControlEvent | undefined {
  let v: unknown;
  try {
    v = JSON.parse(data);
  } catch {
    return undefined;
  }
  if (!isRecord(v) || typeof v.type !== "string" || !KNOWN_EVENTS.includes(v.type)) return undefined;
  return v as unknown as ControlEvent;
}

/** `severity` info never changes the icon (RUL-18). */
export function isDegrading(w: Warning): boolean {
  return w.severity === "warning" || w.severity === "error";
}
