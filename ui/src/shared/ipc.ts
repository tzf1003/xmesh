// The only surface the renderer gets: a fixed set of operations, each with a
// validated argument. There is deliberately no generic `invoke`, no raw
// channel access, and nothing that reaches the filesystem or a subprocess with
// renderer-chosen input.

import type {
  AuditQuery,
  AuditResponse,
  CallEventData,
  ConfigDiagnostic,
  ConfigPutResponse,
  ConfigResponse,
  DrainMode,
  DrainResponse,
  GatewayUpdateRequest,
  GatewayUpdateResponse,
  IngressClientsResponse,
  IngressPasswordResponse,
  PairConnectResponse,
  PairDiscoveredResponse,
  PairDocument,
  PairExportResponse,
  PairImportResponse,
  PairPasswordState,
  PatList,
  PatSecret,
  SessionsResponse,
} from "./control";
import type { DisplayPrefs } from "./display";
import { isAccessEntry, isGatewayHost, isListen } from "./gateway";
import type { MeshRequest, MeshResultFor } from "./mesh";
import type { Snapshot } from "./state";

/** `ipcMain.handle` channels. The preload script hard-codes the same strings; a unit test checks they agree. */
export const CHANNELS = {
  getSnapshot: "xmesh:get-snapshot",
  drain: "xmesh:drain",
  restartKernel: "xmesh:restart-kernel",
  readDenyFile: "xmesh:read-deny-file",
  writeDenyFile: "xmesh:write-deny-file",
  createDenyFile: "xmesh:create-deny-file",
  reloadConfig: "xmesh:reload-config",
  resume: "xmesh:resume",
  startKernel: "xmesh:start-kernel",
  restartUnresponsive: "xmesh:restart-unresponsive",
  selectInstance: "xmesh:select-instance",
  copy: "xmesh:copy",
  copyGateway: "xmesh:copy-gateway",
  updateGateway: "xmesh:update-gateway",
  rotateGatewayPat: "xmesh:rotate-gateway-pat",
  openPath: "xmesh:open-path",
  setLoginItem: "xmesh:set-login-item",
  setDisplay: "xmesh:set-display",
  openExternal: "xmesh:open-external",
  getSessions: "xmesh:get-sessions",
  getConfig: "xmesh:get-config",
  putConfig: "xmesh:put-config",
  patchConfig: "xmesh:patch-config",
  listPats: "xmesh:list-pats",
  createPat: "xmesh:create-pat",
  rotatePat: "xmesh:rotate-pat",
  deletePat: "xmesh:delete-pat",
  listIngressClients: "xmesh:list-ingress-clients",
  revokeIngressClient: "xmesh:revoke-ingress-client",
  rotateIngressPassword: "xmesh:rotate-ingress-password",
  exportPair: "xmesh:export-pair",
  importPair: "xmesh:import-pair",
  getPairPassword: "xmesh:get-pair-password",
  setPairPassword: "xmesh:set-pair-password",
  clearPairPassword: "xmesh:clear-pair-password",
  discoverPairs: "xmesh:discover-pairs",
  connectPair: "xmesh:connect-pair",
  queryAudit: "xmesh:query-audit",
  getRecentCalls: "xmesh:get-recent-calls",
  previewSetup: "xmesh:preview-setup",
  runSetup: "xmesh:run-setup",
  pickPath: "xmesh:pick-path",
  mesh: "xmesh:mesh",
} as const;

/** Main -> renderer push channel. */
export const SNAPSHOT_EVENT = "xmesh:snapshot";
/** Main -> renderer push channel: the newest `call` events (metadata only), newest last. */
export const CALLS_EVENT = "xmesh:calls";

/**
 * Connect text for the local gateway. Each one carries the gateway token, which the main process reads
 * from the token file; the renderer only names the kind and the address.
 */
export const GATEWAY_COPY_KINDS = ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const;
export type GatewayCopyKind = (typeof GATEWAY_COPY_KINDS)[number];

export const COPY_KINDS = [...GATEWAY_COPY_KINDS, "meshLoginRead", "meshLoginWrite", "meshLoginReadHeadscale", "meshLoginWriteHeadscale"] as const;
export type CopyKind = (typeof COPY_KINDS)[number];

/** Copy one gateway address in one of the connect formats. `url` must be an address the kernel reports now. */
export interface GatewayCopyRequest {
  kind: GatewayCopyKind;
  url: string;
}

/** What rotating the gateway token reports: the PAT and its new modification time. The token itself is written to the file and never returned. */
export interface GatewayPatRotated {
  name: string;
  modified_at: string | null;
}

export const PATH_KINDS = ["config", "logs", "audit"] as const;
export type PathKind = (typeof PATH_KINDS)[number];

/** Link targets are chosen by key, never by URL, so the renderer cannot open arbitrary sites. */
export const EXTERNAL_LINKS = {
  docs: "https://github.com/tzf1003/xmesh#readme",
} as const;
export type ExternalKey = keyof typeof EXTERNAL_LINKS;

/** What the native chooser selects: the workspace is a directory; the others are files (`authkeySave` names one to be created). */
export const PICK_KINDS = ["workspace", "authkeyFile", "authkeySave", "allowlistFile", "tokenFile"] as const;
export type PickKind = (typeof PICK_KINDS)[number];

/** What the renderer learns about a failed operation. `diagnostics` and `current_etag` come from the kernel's error body (CTR-CTL-02). */
export interface ActionError {
  code: string;
  message: string;
  reason?: string;
  field?: string;
  current_etag?: string;
  /** rate_limited: how long to wait before trying again. */
  retry_after_ms?: number;
  diagnostics?: ConfigDiagnostic[];
}

export type ActionResult = { ok: true } | { ok: false; error: ActionError };
export type DataResult<T> = { ok: true; data: T } | { ok: false; error: ActionError };

export interface ConfigSaveRequest {
  /** Complete file text, at most 1 MiB. */
  content: string;
  /** The `etag` of the `ConfigResponse` the edit started from; sent as `If-Match`. */
  etag: string;
  /** Validate and diff only; nothing is written. */
  dryRun: boolean;
}

/**
 * One edit of `PATCH /v1/config`. `value` is any JSON value (the schema does not constrain it), so it is
 * typed `unknown` here; the generated `ConfigEdit` narrows it to an object, which arrays and strings are not.
 */
export interface ConfigPatchEdit {
  /** JSON Pointer that starts with "/" and names at least one key. */
  path: string;
  value?: unknown;
  delete?: boolean;
}

/** Generic write helper: edits in, the kernel's diff out. Callers never see the HTTP details. */
export interface ConfigPatchRequest {
  /** 1 to 64 edits, applied in order to the file on disk; all or nothing. */
  edits: ConfigPatchEdit[];
  /** The `etag` of the `ConfigResponse` the edits were computed from; sent as `If-Match`. */
  etag: string;
  /** Validate and diff only; nothing is written. */
  dryRun: boolean;
}

export interface PatCreateArgs {
  name: string;
  scopes: string[];
}

export interface PairImportArgs {
  pair: PairDocument;
  /** The fingerprint the user typed after reading it on the other device. */
  fingerprint: string;
  tags?: string[];
}

/** Pairing with a device found on the LAN or typed in. The password is the other device's connection password. */
export interface PairConnectArgs {
  /** Host or host:port; the kernel uses port 7433 when none is given. */
  address?: string;
  /** The certificate fingerprint the user saw in discovery; the kernel refuses any other certificate. */
  fingerprint?: string;
  password: string;
}

export interface SetupParams {
  nodeName: string;
  mode: "lan" | "tailnet";
  roles: ("agent" | "gateway")[];
  /** Absolute directory the agent may work in (`agent.cwd_policy.allow` and `default_cwd`). */
  workspace: string;
  /** tailnet only. */
  controlUrl?: string;
  /** tailnet only: absolute path of a file that holds the auth key. */
  authkeyFile?: string;
}

export interface SetupStep {
  id: "write-config" | "install" | "start";
  ok: boolean;
  /** One line for people; never command output with secrets (none is expected). */
  detail: string;
}

export interface SetupReport {
  configPath: string;
  steps: SetupStep[];
}

/** What the first run would write, exactly as `runSetup` writes it. Nothing is written by asking. */
export interface SetupPreview {
  configPath: string;
  /** The deny list file the config names; written with the defaults when it does not exist yet. */
  denyPath: string;
  config: string;
}

/** What `window.xmesh` looks like in the renderer. Method names are the whitelist. */
export interface PanelApi {
  getSnapshot(): Promise<Snapshot>;
  onSnapshot(listener: (snapshot: Snapshot) => void): () => void;
  /** `keep` (default) refuses new calls; `kill` also ends every PTY and lease. */
  drain(mode?: DrainMode): Promise<ActionResult>;
  resume(): Promise<ActionResult>;
  /** Starts the service of a node whose config exists but whose kernel is not running. */
  startKernel(node: string): Promise<ActionResult>;
  /** Restarts the kernel of the selected node, as the tray menu does. Asks nothing: the caller confirms first. */
  restartKernel(): Promise<ActionResult>;
  /** Restarts the service of a node whose kernel does not answer. */
  restartUnresponsive(node: string): Promise<ActionResult>;
  selectInstance(id: string): Promise<ActionResult>;
  copy(kind: CopyKind): Promise<ActionResult>;
  copyGateway(request: GatewayCopyRequest): Promise<ActionResult>;
  openPath(kind: PathKind): Promise<ActionResult>;
  setLoginItem(enabled: boolean): Promise<ActionResult>;
  /** Saves the panel's text size and typefaces on this machine and applies them at once. */
  setDisplay(prefs: DisplayPrefs): Promise<ActionResult>;
  openExternal(key: ExternalKey): Promise<ActionResult>;
  getSessions(): Promise<DataResult<SessionsResponse>>;
  getConfig(): Promise<DataResult<ConfigResponse>>;
  putConfig(request: ConfigSaveRequest): Promise<DataResult<ConfigPutResponse>>;
  /** Applies JSON Pointer edits to the config on disk (`PATCH /v1/config`); the form-style settings write through here. */
  patchConfig(request: ConfigPatchRequest): Promise<DataResult<ConfigPutResponse>>;
  /** The deny list file the config names; the renderer never supplies its path. */
  readDenyFile(): Promise<DataResult<DenyFileResponse>>;
  writeDenyFile(request: DenyFileSaveRequest): Promise<DataResult<DenyFileSaved>>;
  /** Creates the deny list file beside the config, for a config that names none yet; the path is the kernel config's directory plus `cwd-deny.yaml`. */
  createDenyFile(request: DenyFileCreateRequest): Promise<DataResult<DenyFileCreated>>;
  /** Asks the kernel to reload its config in place (control API 1.2). A kernel without the route answers with an error. */
  reloadConfig(): Promise<DataResult<ConfigReloadResponse>>;
  listPats(): Promise<DataResult<PatList>>;
  createPat(args: PatCreateArgs): Promise<DataResult<PatSecret>>;
  rotatePat(name: string): Promise<DataResult<PatSecret>>;
  /** Rotates the PAT named "default" (the gateway token). The new token is never returned to the page. */
  rotateGatewayPat(): Promise<DataResult<GatewayPatRotated>>;
  deletePat(name: string): Promise<ActionResult>;
  /** Replaces the gateway's reach settings (PUT /v1/gateway). */
  updateGateway(request: GatewayUpdateRequest): Promise<DataResult<GatewayUpdateResponse>>;
  listIngressClients(): Promise<DataResult<IngressClientsResponse>>;
  revokeIngressClient(clientId: string): Promise<ActionResult>;
  rotateIngressPassword(password: string): Promise<DataResult<IngressPasswordResponse>>;
  exportPair(): Promise<DataResult<PairExportResponse>>;
  importPair(args: PairImportArgs): Promise<DataResult<PairImportResponse>>;
  getPairPassword(): Promise<DataResult<PairPasswordState>>;
  /** Sets or changes this node's connection password. The value is write-only: nothing comes back that contains it. */
  setPairPassword(password: string): Promise<DataResult<PairPasswordState>>;
  clearPairPassword(): Promise<ActionResult>;
  discoverPairs(): Promise<DataResult<PairDiscoveredResponse>>;
  connectPair(args: PairConnectArgs): Promise<DataResult<PairConnectResponse>>;
  queryAudit(query: AuditQuery): Promise<DataResult<AuditResponse>>;
  getRecentCalls(): Promise<CallEventData[]>;
  onCalls(listener: (calls: CallEventData[]) => void): () => void;
  previewSetup(params: SetupParams): Promise<DataResult<SetupPreview>>;
  runSetup(params: SetupParams): Promise<DataResult<SetupReport>>;
  /** Opens the system file chooser; `path` is null when the user cancels. */
  pickPath(kind: PickKind): Promise<DataResult<{ path: string | null }>>;
  /** Tailnet administration through `xmesh mesh ... --json`; credentials stay in the CLI's keychain profile. */
  mesh<R extends MeshRequest>(request: R): Promise<DataResult<MeshResultFor<R>>>;
}

export const PANEL_API_METHODS = [
  "getSnapshot",
  "onSnapshot",
  "drain",
  "resume",
  "startKernel",
  "restartKernel",
  "readDenyFile",
  "writeDenyFile",
  "createDenyFile",
  "reloadConfig",
  "restartUnresponsive",
  "selectInstance",
  "copy",
  "copyGateway",
  "openPath",
  "setLoginItem",
  "setDisplay",
  "openExternal",
  "getSessions",
  "getConfig",
  "putConfig",
  "patchConfig",
  "listPats",
  "createPat",
  "rotatePat",
  "rotateGatewayPat",
  "deletePat",
  "updateGateway",
  "listIngressClients",
  "revokeIngressClient",
  "rotateIngressPassword",
  "exportPair",
  "importPair",
  "getPairPassword",
  "setPairPassword",
  "clearPairPassword",
  "discoverPairs",
  "connectPair",
  "queryAudit",
  "getRecentCalls",
  "onCalls",
  "previewSetup",
  "runSetup",
  "pickPath",
  "mesh",
] as const satisfies readonly (keyof PanelApi)[];

export const PANEL_API_GLOBAL = "xmesh";

// Argument validators. Each returns the parsed value or undefined.

const INSTANCE_ID = /^[A-Za-z0-9._-]{1,128}$/;

export function parseInstanceId(v: unknown): string | undefined {
  return typeof v === "string" && INSTANCE_ID.test(v) && v !== "." && v !== ".." ? v : undefined;
}

export function parseCopyKind(v: unknown): CopyKind | undefined {
  return (COPY_KINDS as readonly unknown[]).includes(v) ? (v as CopyKind) : undefined;
}

export function isGatewayCopyKind(v: unknown): v is GatewayCopyKind {
  return (GATEWAY_COPY_KINDS as readonly unknown[]).includes(v);
}

export function parseGatewayCopy(v: unknown): GatewayCopyRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["kind", "url"])) return undefined;
  if (!isGatewayCopyKind(v.kind) || typeof v.url !== "string" || v.url.length > 512) return undefined;
  return { kind: v.kind, url: v.url };
}

const GATEWAY_LIST_MAX = 64;

function uniqueStrings(v: unknown, valid: (s: string) => boolean): string[] | undefined {
  if (!Array.isArray(v) || v.length > GATEWAY_LIST_MAX) return undefined;
  const out: string[] = [];
  for (const x of v) {
    if (typeof x !== "string" || !valid(x) || out.includes(x)) return undefined;
    out.push(x);
  }
  return out;
}

/** The whole reach replacement: a listen value the contract allows, and valid, distinct access entries and host names. */
export function parseGatewayUpdate(v: unknown): GatewayUpdateRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["listen", "access", "hosts"])) return undefined;
  if (typeof v.listen !== "string" || !isListen(v.listen)) return undefined;
  const access = uniqueStrings(v.access, isAccessEntry);
  const hosts = uniqueStrings(v.hosts, isGatewayHost);
  if (access === undefined || hosts === undefined) return undefined;
  return { listen: v.listen, access, hosts };
}

export function parsePathKind(v: unknown): PathKind | undefined {
  return (PATH_KINDS as readonly unknown[]).includes(v) ? (v as PathKind) : undefined;
}

export function parseExternalKey(v: unknown): ExternalKey | undefined {
  return typeof v === "string" && Object.prototype.hasOwnProperty.call(EXTERNAL_LINKS, v)
    ? (v as ExternalKey)
    : undefined;
}

export function parsePickKind(v: unknown): PickKind | undefined {
  return (PICK_KINDS as readonly unknown[]).includes(v) ? (v as PickKind) : undefined;
}

export function parseBoolean(v: unknown): boolean | undefined {
  return typeof v === "boolean" ? v : undefined;
}

// Validators for the panel's data operations. The kernel validates again, but
// the main process refuses malformed input first so that nothing odd is
// forwarded on the renderer's behalf. Patterns mirror the contract schemas.

const NODE_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const TAG = /^tag:[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const SCOPE_SEG = "(\\*|[A-Za-z0-9][A-Za-z0-9-]{0,62})";
const SCOPE = new RegExp(
  `^(devices:read|exec:${SCOPE_SEG}|pty:${SCOPE_SEG}|fs:${SCOPE_SEG}|fs:(read|write):${SCOPE_SEG}|sys:${SCOPE_SEG}|backend:${SCOPE_SEG}:${SCOPE_SEG})$`,
);
const ETAG = /^sha256-[0-9a-f]{64}$/;
const FINGERPRINT = /^([0-9A-Fa-f]{2}:?){31}[0-9A-Fa-f]{2}$/;
const CLIENT_ID = /^[\x21-\x7e]{1,256}$/;
const TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/;
const PAIR_TAGS = ["tag:peer", "tag:device", "tag:ingress"] as const;

/** PUT /v1/config accepts at most 1 MiB; this counts bytes, not UTF-16 units. */
export const CONFIG_MAX_BYTES = 1024 * 1024;
export const PASSWORD_MIN = 12;
export const PASSWORD_MAX = 256;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Only these keys, nothing else: a stray key from a confused renderer is an error, not ignored. */
function onlyKeys(v: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(v).every((k) => allowed.includes(k));
}

export function parseDrainMode(v: unknown): DrainMode | undefined {
  return v === "keep" || v === "kill" ? v : undefined;
}

export function parseNodeName(v: unknown): string | undefined {
  return typeof v === "string" && NODE_NAME.test(v) ? v : undefined;
}

export function parseScope(v: unknown): string | undefined {
  return typeof v === "string" && SCOPE.test(v) ? v : undefined;
}

/** The deny list file as the config names it. `path` is absent when the config names none. */
export interface DenyFileResponse {
  path?: string;
  /** Whether the file exists; a configured file that is missing stops the agent role from starting. */
  exists: boolean;
  text: string;
  /** Empty when the file does not exist, otherwise `sha256-<hex>` of `text`. */
  etag: string;
}

export interface DenyFileSaveRequest {
  /** Complete file text, at most 64 KiB. */
  text: string;
  /** The `etag` the edit started from; the save fails if the file changed since. */
  etag: string;
}

export interface DenyFileSaved {
  etag: string;
}
export interface DenyFileCreateRequest {
  /** Complete file text, at most 64 KiB. */
  text: string;
}
export interface DenyFileCreated {
  /** Absolute path of the new file; the config names it in `agent.cwd_policy.deny_file`. */
  path: string;
  etag: string;
}
/** The kernel's answer to a reload (control API 1.2): what it applied and what still needs a restart. */
export interface ConfigReloadResponse {
  applied: boolean;
  valid: boolean;
  load_error?: string | null;
  changed_paths: string[];
  restart_required: boolean;
  restart_paths: string[];
  /** Valid settings the running kernel could not put into effect, such as a listen address that is taken (control API 1.6). Absent from older kernels. */
  apply_errors?: { section: string; message: string }[];
}

export const DENY_FILE_MAX_BYTES = 64 * 1024;

export function parseDenyFileSave(v: unknown): DenyFileSaveRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["text", "etag"])) return undefined;
  const { text, etag } = v;
  if (typeof text !== "string" || new TextEncoder().encode(text).length > DENY_FILE_MAX_BYTES) return undefined;
  if (typeof etag !== "string" || !/^(sha256-[0-9a-f]{64})?$/.test(etag)) return undefined;
  return { text, etag };
}

export function parseDenyFileCreate(v: unknown): DenyFileCreateRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["text"])) return undefined;
  const { text } = v;
  if (typeof text !== "string" || new TextEncoder().encode(text).length > DENY_FILE_MAX_BYTES) return undefined;
  return { text };
}

export function parseConfigSave(v: unknown): ConfigSaveRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["content", "etag", "dryRun"])) return undefined;
  const { content, etag, dryRun } = v;
  if (typeof content !== "string" || new TextEncoder().encode(content).length > CONFIG_MAX_BYTES) return undefined;
  if (typeof etag !== "string" || !ETAG.test(etag)) return undefined;
  if (typeof dryRun !== "boolean") return undefined;
  return { content, etag, dryRun };
}

/** The contract's limit on edits per PATCH. */
export const CONFIG_PATCH_MAX_EDITS = 64;
const POINTER_MAX = 512;

/** A JSON value with finite numbers only, nested at most 64 levels deep. Only arrays and plain objects count as containers. */
export function isJsonValue(v: unknown, depth = 0): boolean {
  if (depth > 64) return false;
  if (v === null || typeof v === "string" || typeof v === "boolean") return true;
  if (typeof v === "number") return Number.isFinite(v);
  if (Array.isArray(v)) return v.every((x) => isJsonValue(x, depth + 1));
  if (!isRecord(v)) return false;
  const proto = Object.getPrototypeOf(v) as unknown;
  if (proto !== Object.prototype && proto !== null) return false;
  return Object.values(v).every((x) => isJsonValue(x, depth + 1));
}

/** One edit: a pointer with at least one segment, then either `value` or `delete: true`, never both. */
function parsePatchEdit(v: unknown): ConfigPatchEdit | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["path", "value", "delete"])) return undefined;
  const { path } = v;
  if (typeof path !== "string" || !path.startsWith("/") || path.length < 2 || path.length > POINTER_MAX) return undefined;
  if (/[\x00-\x1f\x7f]/.test(path)) return undefined;
  if (v.delete !== undefined && typeof v.delete !== "boolean") return undefined;
  const remove = v.delete === true;
  if (remove) return "value" in v ? undefined : { path, delete: true };
  if (!("value" in v) || !isJsonValue(v.value)) return undefined;
  return { path, value: v.value };
}

/** Structural check only: the kernel validates the resulting configuration and answers 422 when it is wrong. */
export function parseConfigPatch(v: unknown): ConfigPatchRequest | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["edits", "etag", "dryRun"])) return undefined;
  const { edits, etag, dryRun } = v;
  if (!Array.isArray(edits) || edits.length === 0 || edits.length > CONFIG_PATCH_MAX_EDITS) return undefined;
  const parsed: ConfigPatchEdit[] = [];
  for (const e of edits) {
    const edit = parsePatchEdit(e);
    if (edit === undefined) return undefined;
    parsed.push(edit);
  }
  if (typeof etag !== "string" || !ETAG.test(etag)) return undefined;
  if (typeof dryRun !== "boolean") return undefined;
  const out: ConfigPatchRequest = { edits: parsed, etag, dryRun };
  // The body is sent as JSON, so the same byte limit as a whole-file save applies.
  if (new TextEncoder().encode(JSON.stringify(out)).length > CONFIG_MAX_BYTES) return undefined;
  return out;
}

export function parsePatCreate(v: unknown): PatCreateArgs | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["name", "scopes"])) return undefined;
  const name = parseNodeName(v.name);
  if (name === undefined || !Array.isArray(v.scopes) || v.scopes.length === 0 || v.scopes.length > 64) return undefined;
  const scopes: string[] = [];
  for (const s of v.scopes) {
    const scope = parseScope(s);
    if (scope === undefined || scopes.includes(scope)) return undefined;
    scopes.push(scope);
  }
  return { name, scopes };
}

export function parseClientId(v: unknown): string | undefined {
  return typeof v === "string" && CLIENT_ID.test(v) ? v : undefined;
}

// Go's unicode.IsSpace, which the kernel's strings.TrimSpace uses. ECMAScript trim() differs (it
// strips U+FEFF and keeps U+0085), so the set is spelled out.
const EDGE_SPACE = /^[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]|[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]$/;

/** The kernel trims the password file on read, so edge whitespace would not survive a restart and is refused. */
export function hasEdgeSpace(v: string): boolean {
  return EDGE_SPACE.test(v);
}

export function parsePassword(v: unknown): string | undefined {
  if (typeof v !== "string" || hasEdgeSpace(v)) return undefined;
  const n = [...v].length;
  return n >= PASSWORD_MIN && n <= PASSWORD_MAX ? v : undefined;
}

/** The kernel's connection-password rules (docs/contracts/control.md CTR-CTL-08): 10 to 128 characters, no edge whitespace. */
export const PAIR_PASSWORD_MIN = 10;
export const PAIR_PASSWORD_MAX = 128;

export function parsePairPassword(v: unknown): string | undefined {
  if (typeof v !== "string" || hasEdgeSpace(v)) return undefined;
  const n = [...v].length;
  return n >= PAIR_PASSWORD_MIN && n <= PAIR_PASSWORD_MAX ? v : undefined;
}

/** A fingerprint as the user may copy it, with or without colons and in any case; returned in the kernel's form. */
function cleanFingerprint(v: unknown): string | undefined {
  if (typeof v !== "string" || !FINGERPRINT.test(v)) return undefined;
  return v.replace(/:/g, "").toLowerCase();
}

export function parsePairConnect(v: unknown): PairConnectArgs | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["address", "fingerprint", "password"])) return undefined;
  const password = parsePairPassword(v.password);
  if (password === undefined) return undefined;
  const out: PairConnectArgs = { password };
  if (v.address !== undefined) {
    if (typeof v.address !== "string" || v.address.trim() === "" || v.address.length > 260) return undefined;
    out.address = v.address.trim();
  }
  if (v.fingerprint !== undefined) {
    const fp = cleanFingerprint(v.fingerprint);
    if (fp === undefined) return undefined;
    out.fingerprint = fp;
  }
  if (out.address === undefined && out.fingerprint === undefined) return undefined;
  return out;
}

export function parsePairImport(v: unknown): PairImportArgs | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["pair", "fingerprint", "tags"])) return undefined;
  const { pair, fingerprint, tags } = v;
  if (!isRecord(pair) || !onlyKeys(pair, ["name", "fingerprint", "certificate_pem", "tags"])) return undefined;
  if (parseNodeName(pair.name) === undefined) return undefined;
  if (typeof pair.fingerprint !== "string" || typeof pair.certificate_pem !== "string") return undefined;
  if (!pair.certificate_pem.startsWith("-----BEGIN CERTIFICATE-----") || pair.certificate_pem.length > 16 * 1024) return undefined;
  // `xmesh pair --export` of earlier kernels wrote `"tags": null`, and a hand-trimmed file may omit it; the importer picks the roles anyway.
  const pairTags = pair.tags ?? [];
  if (!Array.isArray(pairTags) || !pairTags.every((t) => typeof t === "string" && TAG.test(t))) return undefined;
  if (typeof fingerprint !== "string" || !FINGERPRINT.test(fingerprint)) return undefined;
  const out: PairImportArgs = {
    pair: {
      name: pair.name as string,
      fingerprint: pair.fingerprint,
      certificate_pem: pair.certificate_pem,
      tags: pairTags as string[],
    },
    fingerprint,
  };
  if (tags !== undefined) {
    if (!Array.isArray(tags) || tags.length === 0) return undefined;
    const picked: string[] = [];
    for (const t of tags) {
      if (!(PAIR_TAGS as readonly unknown[]).includes(t) || picked.includes(t as string)) return undefined;
      picked.push(t as string);
    }
    out.tags = picked;
  }
  return out;
}

const AUDIT_EVENTS = [
  "tool_call",
  "pty_open",
  "pty_close",
  "pty_transfer",
  "pty_signal",
  "pty_write",
  "pty_io",
  "authz_deny",
  "policy_reload_failed",
  "progress_unrouted",
  "audit_dropped",
  "control_call",
] as const;

export function parseAuditQuery(v: unknown): AuditQuery | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["since", "until", "limit", "cursor", "event", "tool", "device", "caller", "status"])) {
    return undefined;
  }
  const out: AuditQuery = {};
  const text = (x: unknown, max: number): string | undefined =>
    typeof x === "string" && x.length >= 1 && x.length <= max && !/[\x00-\x1f]/.test(x) ? x : undefined;
  if (v.since !== undefined) {
    if (typeof v.since !== "string" || !TIMESTAMP.test(v.since)) return undefined;
    out.since = v.since;
  }
  if (v.until !== undefined) {
    if (typeof v.until !== "string" || !TIMESTAMP.test(v.until)) return undefined;
    out.until = v.until;
  }
  if (v.limit !== undefined) {
    if (typeof v.limit !== "string" || !/^([1-9][0-9]{0,2}|1000)$/.test(v.limit)) return undefined;
    out.limit = v.limit;
  }
  if (v.cursor !== undefined) {
    const c = text(v.cursor, 2048);
    if (c === undefined) return undefined;
    out.cursor = c;
  }
  if (v.event !== undefined) {
    if (typeof v.event !== "string" || !(AUDIT_EVENTS as readonly string[]).includes(v.event)) return undefined;
    out.event = v.event;
  }
  if (v.tool !== undefined) {
    const t = text(v.tool, 256);
    if (t === undefined) return undefined;
    out.tool = t;
  }
  if (v.device !== undefined) {
    const d = parseNodeName(v.device);
    if (d === undefined) return undefined;
    out.device = d;
  }
  if (v.caller !== undefined) {
    const c = text(v.caller, 256);
    if (c === undefined) return undefined;
    out.caller = c;
  }
  if (v.status !== undefined) {
    if (v.status !== "ok" && v.status !== "error") return undefined;
    out.status = v.status;
  }
  return out;
}

export { AUDIT_EVENTS };
