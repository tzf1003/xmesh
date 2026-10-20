import { isAbsolutePath, isHttpsUrl, NODE_NAME } from "./setup";
import { hasEdgeSpace } from "./ipc";

export type Purpose = "local" | "multi-device" | "cloud";
export type ClientId = "codex" | "claude-code";
export type { Input as OnboardingInput, Plan as OnboardingPlan, Job as OnboardingJob, Inspection as OnboardingInspect, Step as OnboardingStep } from "./generated/onboarding";
import type { Input as OnboardingInput, Plan as OnboardingPlan, Job as OnboardingJob, Inspection as OnboardingInspect } from "./generated/onboarding";
export type OnboardingRequest =
  | { op: "inspect" }
  | { op: "doctor"; copy?: boolean }
  | { op: "plan"; input: OnboardingInput }
  | { op: "set_password"; job: string; password: string }
  | { op: "apply"; plan: string }
  | { op: "status" | "resume" | "verify" | "cancel"; job: string; open_auth?: boolean; copy_prompt?: boolean };
export type OnboardingResultFor<R extends OnboardingRequest> = R extends { op: "inspect" } ? OnboardingInspect : R extends { op: "plan" } ? OnboardingPlan : R extends { op: "doctor" } ? Record<string, unknown> : OnboardingJob;
const record = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === "object" && !Array.isArray(v);
const keys = (v: Record<string, unknown>, allowed: string[]): boolean => Object.keys(v).every((key) => allowed.includes(key));
const ID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
export function parseOnboardingInput(v: unknown): OnboardingInput | undefined {
  if (!record(v) || !keys(v, ["schema_version", "purpose", "node_name", "workspace", "permission", "clients", "autostart", "network"])) return;
  if (v.schema_version !== 1 || !["local", "multi-device", "cloud"].includes(String(v.purpose)) || typeof v.node_name !== "string" || !NODE_NAME.test(v.node_name)) return;
  if (typeof v.workspace !== "string" || v.workspace.length > 4096 || /[\x00-\x1f]/.test(v.workspace) || !isAbsolutePath(v.workspace)) return;
  if (!["development", "readonly"].includes(String(v.permission)) || typeof v.autostart !== "boolean") return;
  if (!Array.isArray(v.clients) || v.clients.length > 2 || new Set(v.clients).size !== v.clients.length || !v.clients.every((x) => x === "codex" || x === "claude-code")) return;
  const n = v.network;
  if (!record(n) || !keys(n, ["mode", "control_url", "auth_method", "authkey_file"]) || !["lan", "tailnet", "coexist"].includes(String(n.mode)) || n.auth_method !== undefined && !["browser", "key"].includes(String(n.auth_method))) return;
  if (n.control_url !== undefined && (typeof n.control_url !== "string" || n.control_url.length > 2048 || !isHttpsUrl(n.control_url))) return;
  if (n.authkey_file !== undefined && (typeof n.authkey_file !== "string" || n.authkey_file.length > 4096 || !isAbsolutePath(n.authkey_file) || /[\x00-\x1f]/.test(n.authkey_file))) return;
  if (n.mode === "lan" && (n.control_url !== undefined || n.auth_method !== undefined || n.authkey_file !== undefined)) return;
  if (n.mode !== "lan" && n.auth_method === "key" && !n.authkey_file) return;
  if (v.purpose === "local" && n.mode !== "lan" || v.purpose === "cloud" && n.mode !== "tailnet") return;
  return v as unknown as OnboardingInput;
}
export function parseOnboarding(v: unknown): OnboardingRequest | undefined {
  if (!record(v)) return;
  if (v.op === "inspect" && keys(v, ["op"])) return { op: "inspect" };
  if (v.op === "doctor" && keys(v, ["op", "copy"]) && (v.copy === undefined || typeof v.copy === "boolean")) return v as unknown as OnboardingRequest;
  if (v.op === "plan" && keys(v, ["op", "input"])) { const input = parseOnboardingInput(v.input); return input ? { op: "plan", input } : undefined; }
  if (v.op === "set_password" && keys(v, ["op", "job", "password"]) && typeof v.job === "string" && ID.test(v.job) && validIngressPassword(v.password)) return v as unknown as OnboardingRequest;
  if (v.op === "apply" && keys(v, ["op", "plan"]) && typeof v.plan === "string" && ID.test(v.plan)) return { op: "apply", plan: v.plan };
  if (["status", "resume", "verify", "cancel"].includes(String(v.op)) && keys(v, ["op", "job", "open_auth", "copy_prompt"]) && typeof v.job === "string" && ID.test(v.job) && (v.open_auth === undefined || v.op === "resume" && typeof v.open_auth === "boolean") && (v.copy_prompt === undefined || v.op === "status" && typeof v.copy_prompt === "boolean")) return v as unknown as OnboardingRequest;
}
/** Redact credential-shaped values before they reach the panel or clipboard. */
export function redactOnboarding(text: string): string {
  return text.replace(/(?:tskey|hskey)-[A-Za-z0-9_-]+/g, "[redacted]").replace(/(Bearer\s+)[^\s"']+/gi, "$1[redacted]").replace(/((?:token|password|secret|authkey)["']?\s*[:=]\s*["']?)[^\s,"']+/gi, "$1[redacted]");
}

/** Structured responses preserve paths and identifiers; only secret fields are removed. */
export function redactOnboardingResponse<T>(value: T): T {
  if (Array.isArray(value)) return value.map(redactOnboardingResponse) as T;
  if (!record(value)) return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, /^(?:password|token|secret|authkey|auth_key|access_token|refresh_token|client_secret)$/i.test(key) ? "[redacted]" : redactOnboardingResponse(item)])) as T;
}

export function setupNeedsAttention(job: OnboardingJob): boolean {
  return job.status !== "cancelled" && (job.status !== "succeeded" || !job.readiness.client_verified);
}

export function verificationPrompt(job: OnboardingJob): string | undefined {
  return job.verification_tool && job.verification_path ? `Call xmesh tool ${job.verification_tool} with ${JSON.stringify({ path: job.verification_path })}. Setup job: ${job.id}.` : undefined;
}

export function validIngressPassword(value: unknown): value is string {
  return typeof value === "string" && [...value].length >= 12 && [...value].length <= 256 && !hasEdgeSpace(value) && !/[\x00-\x1f]/.test(value);
}

/** Browser challenge URLs are useful for authorization, while diagnostics only need their presence. */
export function onboardingDiagnostics(value: unknown): string {
  return redactOnboarding(JSON.stringify(value, (key, item) => ["auth_url", "password", "token", "secret", "authkey"].includes(key) ? "[redacted]" : item, 2));
}
