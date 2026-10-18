// Pure helpers behind the CI runners page (CTR-CTL-15): what to call a state, which button an
// instance that needs attention gets, how typed text becomes labels and a repository name.

import type { CiBinding, CiInstance, CiInstanceState, CiProblemCode } from "../../shared/control";
import { CI_LABEL, CI_MAX_LABELS, CI_TARGET, ciNextStep, type CiNextStep } from "../../shared/ci";
import type { Key } from "../messages";

export const STATE_KEY: Record<CiInstanceState, Key> = {
  installing: "ci.state.installing",
  registering: "ci.state.registering",
  starting: "ci.state.starting",
  idle: "ci.state.idle",
  busy: "ci.state.busy",
  backoff: "ci.state.backoff",
  stopping: "ci.state.stopping",
  stopped: "ci.state.stopped",
  broken: "ci.state.broken",
};

export function stateKind(state: CiInstanceState): "" | "ok" | "warn" | "danger" | "info" {
  switch (state) {
    case "idle":
      return "ok";
    case "busy":
      return "info";
    case "broken":
      return "danger";
    case "backoff":
      return "warn";
    default:
      return "";
  }
}

export const RESULT_KEY = {
  succeeded: "ci.result.succeeded",
  failed: "ci.result.failed",
  canceled: "ci.result.canceled",
  skipped: "ci.result.skipped",
  abandoned: "ci.result.abandoned",
  unknown: "ci.result.unknown",
} as const satisfies Record<string, Key>;

export const STEP_KEY: Record<Exclude<CiNextStep, "none">, Key> = {
  restart: "ci.step.restart",
  repair: "ci.step.repair",
  account: "ci.step.account",
};

/** The button an instance with a problem gets, or undefined when it needs nothing from the user. */
export function stepOf(instance: CiInstance): Exclude<CiNextStep, "none"> | undefined {
  const step = ciNextStep(instance);
  return step === "none" ? undefined : step;
}

/** The problem codes whose cure is a fresh registration, which a binding without an account needs a token for. */
export function needsRegistrationToken(binding: CiBinding, code: CiProblemCode | undefined): boolean {
  return code !== undefined && ciNextStep({ problem: { code, message: "" } } as CiInstance) === "repair" && binding.account === undefined;
}

/** A binding as one line: instances running a job over instances in total. */
export function bindingLine(b: CiBinding): { busy: number; total: number } {
  return { busy: b.instances.filter((i) => i.state === "busy").length, total: b.instances.length };
}

/** `owner/repo`, from what the user typed or pasted: the name itself, a web address, or a clone address. */
export function parseRepo(text: string): string | undefined {
  let v = text.trim();
  v = v.replace(/^git@github\.com:/, "").replace(/^https?:\/\/[^/]+\//, "");
  v = v.replace(/\.git$/, "").replace(/\/+$/, "");
  const parts = v.split("/");
  if (parts.length > 2 && parts[2] !== "") v = `${parts[0]}/${parts[1]}`;
  return CI_TARGET.test(v) ? v : undefined;
}

/** Labels typed as a comma or space separated list. `undefined` when one of them is not a valid label or there are too many. */
export function parseLabelInput(text: string): string[] | undefined {
  const out: string[] = [];
  for (const piece of text.split(/[\s,，]+/)) {
    if (piece === "") continue;
    if (!CI_LABEL.test(piece)) return undefined;
    if (!out.includes(piece)) out.push(piece);
  }
  return out.length <= CI_MAX_LABELS ? out : undefined;
}

/** The kernel's rule for a registration token the user copies from the repository's runner page. */
export function registrationTokenValid(v: string): boolean {
  return /^[A-Za-z0-9_]{10,255}$/.test(v.trim());
}

/** The kernel's rule for a GitHub token. */
export function githubTokenValid(v: string): boolean {
  return /^[A-Za-z0-9_]{20,255}$/.test(v.trim());
}

/** The reasons the kernel gives a failed CI request that have their own wording (CTR-CTL-15). */
const FAILURE_KEY: Record<string, Key> = {
  github_rejected: "ci.err.github_rejected",
  no_admin_permission: "ci.err.no_admin_permission",
  github_unreachable: "ci.err.github_unreachable",
  account_in_use: "ci.err.account_in_use",
  account_not_found: "ci.err.account_not_found",
  account_without_token: "ci.err.account_without_token",
  accounts_unavailable: "ci.err.accounts_unavailable",
  too_many_accounts: "ci.err.too_many_accounts",
  not_a_runner: "ci.err.not_a_runner",
  ephemeral_runner: "ci.err.ephemeral_runner",
  service_installed: "ci.err.service_installed",
  runner_running: "ci.err.runner_running",
  import_single_instance: "ci.err.import_single_instance",
};

/** The panel's own sentence for a failed CI request, or undefined to show the kernel's message. */
export function ciFailure(error: { code: string; reason?: string }): Key | undefined {
  if (error.reason !== undefined && FAILURE_KEY[error.reason] !== undefined) return FAILURE_KEY[error.reason];
  return error.code === "conflict" && error.reason === undefined ? "ci.err.conflict" : undefined;
}

/** True when a create failed only because the repository is public and the user has not yet said yes. */
export function needsPublicConfirm(error: { reason?: string }): boolean {
  return error.reason === "public_repo_requires_confirm";
}
