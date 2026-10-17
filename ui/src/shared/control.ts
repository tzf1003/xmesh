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
  if (
    !isRecord(raw) ||
    !isRecord(raw.control) ||
    !isRecord(raw.drain) ||
    !isRecord(raw.paths) ||
    !isRecord(raw.limits) ||
    !isRecord(raw.peers) ||
    !Array.isArray(raw.warnings) ||
    !Array.isArray(raw.roles)
  ) {
    throw new ControlRequestError("internal", "unexpected status response");
  }
  const status = raw as unknown as StatusResponse;
  // Tolerate a `control.capabilities` that is missing: treat as none.
  if (!Array.isArray(status.control.capabilities)) status.control.capabilities = [];
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
