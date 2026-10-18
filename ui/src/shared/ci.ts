// GitHub Actions runner hosting from the panel (docs/contracts/control.md, CTR-CTL-15). The renderer
// sends one validated request object; the main process turns it into a fixed Control API route.
// Tokens (a GitHub token, a one-time registration token) travel in these requests once and are
// never part of any result.

import type {
  CiAccount,
  CiBinding,
  CiImportInspectResponse,
  CiInstance,
  CiOverview,
  CiProblemCode,
  CiReposResponse,
} from "./control";
import { isAbsolutePath } from "./setup";

export const CI_ID = /^[a-z0-9][a-z0-9-]{0,39}$/;
export const CI_TARGET = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/;
export const CI_LABEL = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
/** A GitHub token as the kernel accepts it (CiAccountCreate.token). */
export const CI_TOKEN = /^[A-Za-z0-9_]{20,255}$/;
/** A one-time runner registration token (CiBindingCreate.registration_token). */
export const CI_REGISTRATION_TOKEN = /^[A-Za-z0-9_]{10,255}$/;
export const CI_HOST = /^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$/;
export const CI_MAX_CONCURRENCY = 16;
export const CI_MAX_LABELS = 16;

export type CiBindingWay =
  | { target: string; account: string }
  | { target: string; registration_token: string }
  | { import_dir: string };

export type CiBindingCreateArgs = CiBindingWay & {
  id?: string;
  concurrency?: number;
  labels?: string[];
  confirm_public?: boolean;
};

export interface CiBindingPatchArgs {
  concurrency?: number;
  labels?: string[];
  paused?: boolean;
  account?: string;
}

export type CiRequest =
  | { op: "get" }
  | { op: "account.create"; token: string; host?: string }
  | { op: "account.delete"; id: string }
  | { op: "repos"; id: string; q?: string; page?: string }
  | { op: "binding.create"; args: CiBindingCreateArgs }
  | { op: "binding.patch"; id: string; patch: CiBindingPatchArgs }
  | { op: "binding.delete"; id: string; force?: boolean }
  | { op: "binding.restart"; id: string }
  | { op: "binding.repair"; id: string; registration_token?: string }
  | { op: "import.inspect"; dir: string };

export type CiOp = CiRequest["op"];

export const CI_OPS = [
  "get",
  "account.create",
  "account.delete",
  "repos",
  "binding.create",
  "binding.patch",
  "binding.delete",
  "binding.restart",
  "binding.repair",
  "import.inspect",
] as const satisfies readonly CiOp[];

/** What each request returns; the ones that have no body return `null`. */
export interface CiResults {
  get: CiOverview;
  "account.create": CiAccount;
  "account.delete": null;
  repos: CiReposResponse;
  "binding.create": CiBinding;
  "binding.patch": CiBinding;
  "binding.delete": CiBinding;
  "binding.restart": null;
  "binding.repair": CiBinding;
  "import.inspect": CiImportInspectResponse;
}
export type CiResultFor<R extends CiRequest> = CiResults[R["op"]];

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function onlyKeys(v: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(v).every((k) => allowed.includes(k));
}
const str = (v: unknown, re: RegExp): v is string => typeof v === "string" && re.test(v);

function parseLabels(v: unknown): string[] | undefined {
  if (!Array.isArray(v) || v.length > CI_MAX_LABELS) return undefined;
  const out: string[] = [];
  for (const l of v) {
    if (!str(l, CI_LABEL) || out.includes(l)) return undefined;
    out.push(l);
  }
  return out;
}

function parseConcurrency(v: unknown): number | undefined {
  return typeof v === "number" && Number.isInteger(v) && v >= 1 && v <= CI_MAX_CONCURRENCY ? v : undefined;
}

function parseCreate(v: unknown): CiBindingCreateArgs | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["id", "target", "account", "registration_token", "import_dir", "concurrency", "labels", "confirm_public"])) return undefined;
  const ways = [v.target !== undefined && v.account !== undefined, v.target !== undefined && v.registration_token !== undefined, v.import_dir !== undefined].filter(Boolean).length;
  if (ways !== 1) return undefined;
  let way: CiBindingWay;
  if (v.import_dir !== undefined) {
    if (v.target !== undefined || v.account !== undefined || v.registration_token !== undefined) return undefined;
    if (typeof v.import_dir !== "string" || !isAbsolutePath(v.import_dir)) return undefined;
    way = { import_dir: v.import_dir };
  } else if (v.account !== undefined) {
    if (v.registration_token !== undefined || !str(v.target, CI_TARGET) || !str(v.account, CI_ID)) return undefined;
    way = { target: v.target, account: v.account };
  } else {
    if (!str(v.target, CI_TARGET) || !str(v.registration_token, CI_REGISTRATION_TOKEN)) return undefined;
    way = { target: v.target, registration_token: v.registration_token };
  }
  const out: CiBindingCreateArgs = { ...way };
  if (v.id !== undefined) {
    if (!str(v.id, CI_ID)) return undefined;
    out.id = v.id;
  }
  if (v.concurrency !== undefined) {
    const n = parseConcurrency(v.concurrency);
    if (n === undefined) return undefined;
    out.concurrency = n;
  }
  if (v.labels !== undefined) {
    const labels = parseLabels(v.labels);
    if (labels === undefined) return undefined;
    out.labels = labels;
  }
  if (v.confirm_public !== undefined) {
    if (typeof v.confirm_public !== "boolean") return undefined;
    out.confirm_public = v.confirm_public;
  }
  return out;
}

function parsePatch(v: unknown): CiBindingPatchArgs | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["concurrency", "labels", "paused", "account"])) return undefined;
  const out: CiBindingPatchArgs = {};
  if (v.concurrency !== undefined) {
    const n = parseConcurrency(v.concurrency);
    if (n === undefined) return undefined;
    out.concurrency = n;
  }
  if (v.labels !== undefined) {
    const labels = parseLabels(v.labels);
    if (labels === undefined) return undefined;
    out.labels = labels;
  }
  if (v.paused !== undefined) {
    if (typeof v.paused !== "boolean") return undefined;
    out.paused = v.paused;
  }
  if (v.account !== undefined) {
    if (!str(v.account, CI_ID)) return undefined;
    out.account = v.account;
  }
  return Object.keys(out).length === 0 ? undefined : out;
}

/** Checks a request from the renderer. Anything that is not exactly one of the forms above is refused. */
export function parseCiRequest(v: unknown): CiRequest | undefined {
  if (!isRecord(v) || typeof v.op !== "string") return undefined;
  switch (v.op) {
    case "get":
      return onlyKeys(v, ["op"]) ? { op: "get" } : undefined;
    case "account.create": {
      if (!onlyKeys(v, ["op", "token", "host"]) || !str(v.token, CI_TOKEN)) return undefined;
      if (v.host === undefined) return { op: "account.create", token: v.token };
      return str(v.host, CI_HOST) ? { op: "account.create", token: v.token, host: v.host } : undefined;
    }
    case "account.delete":
      return onlyKeys(v, ["op", "id"]) && str(v.id, CI_ID) ? { op: "account.delete", id: v.id } : undefined;
    case "repos": {
      if (!onlyKeys(v, ["op", "id", "q", "page"]) || !str(v.id, CI_ID)) return undefined;
      const out: CiRequest = { op: "repos", id: v.id };
      if (v.q !== undefined) {
        if (typeof v.q !== "string" || v.q.length > 100) return undefined;
        out.q = v.q;
      }
      if (v.page !== undefined) {
        if (!str(v.page, /^[0-9]{1,4}$/)) return undefined;
        out.page = v.page;
      }
      return out;
    }
    case "binding.create": {
      if (!onlyKeys(v, ["op", "args"])) return undefined;
      const args = parseCreate(v.args);
      return args === undefined ? undefined : { op: "binding.create", args };
    }
    case "binding.patch": {
      if (!onlyKeys(v, ["op", "id", "patch"]) || !str(v.id, CI_ID)) return undefined;
      const patch = parsePatch(v.patch);
      return patch === undefined ? undefined : { op: "binding.patch", id: v.id, patch };
    }
    case "binding.delete": {
      if (!onlyKeys(v, ["op", "id", "force"]) || !str(v.id, CI_ID)) return undefined;
      if (v.force === undefined) return { op: "binding.delete", id: v.id };
      return typeof v.force === "boolean" ? { op: "binding.delete", id: v.id, force: v.force } : undefined;
    }
    case "binding.restart":
      return onlyKeys(v, ["op", "id"]) && str(v.id, CI_ID) ? { op: "binding.restart", id: v.id } : undefined;
    case "binding.repair": {
      if (!onlyKeys(v, ["op", "id", "registration_token"]) || !str(v.id, CI_ID)) return undefined;
      if (v.registration_token === undefined) return { op: "binding.repair", id: v.id };
      return str(v.registration_token, CI_REGISTRATION_TOKEN) ? { op: "binding.repair", id: v.id, registration_token: v.registration_token } : undefined;
    }
    case "import.inspect":
      return onlyKeys(v, ["op", "dir"]) && typeof v.dir === "string" && isAbsolutePath(v.dir) ? { op: "import.inspect", dir: v.dir } : undefined;
  }
  return undefined;
}

// ---- what the panel shows ----

/** The next step the user takes for an instance that needs attention, by problem code (CTR-CTL-15). */
export type CiNextStep = "restart" | "repair" | "account" | "none";

export const CI_NEXT_STEP: Record<CiProblemCode, CiNextStep> = {
  session_conflict: "restart",
  runner_removed: "repair",
  registration_failed: "repair",
  auth_failed: "account",
  download_failed: "restart",
  missing_dependency: "restart",
  start_failed: "restart",
  version_deprecated: "restart",
  unclassified: "restart",
};

export function ciNextStep(instance: CiInstance): CiNextStep {
  return instance.problem ? CI_NEXT_STEP[instance.problem.code] : "none";
}

/** Counts for the tray line: runners running a job, runners waiting for one, and runners that need the user. */
export interface CiCounts {
  busy: number;
  idle: number;
  broken: number;
}

export function ciCounts(bindings: readonly CiBinding[] | undefined): CiCounts {
  const out: CiCounts = { busy: 0, idle: 0, broken: 0 };
  for (const b of bindings ?? []) {
    for (const i of b.instances) {
      if (i.state === "busy") out.busy++;
      else if (i.state === "idle") out.idle++;
      else if (i.state === "broken") out.broken++;
    }
  }
  return out;
}

/** A binding is working on a job now. */
export function ciBusy(bindings: readonly CiBinding[] | undefined): boolean {
  return ciCounts(bindings).busy > 0;
}
