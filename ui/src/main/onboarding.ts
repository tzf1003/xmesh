import { execFile } from "node:child_process";
import type { DataResult } from "../shared/ipc";
import { onboardingDiagnostics, verificationPrompt, redactOnboarding, type OnboardingRequest, type OnboardingJob, type OnboardingPlan, type OnboardingInput, type OnboardingInspect } from "../shared/onboarding";
import { isLoginUrl } from "./browser-login";
import { xmeshBinary } from "./cli";

export function onboardingArgs(request: OnboardingRequest, root: string): string[] {
  if (request.op === "doctor") return ["doctor", "--root", root, "--json"];
  const args = ["setup", request.op === "set_password" ? "ingress-password" : request.op];
  if (request.op === "set_password") args.push("--input", "-");
  if (request.op === "plan") args.push("--input", "-");
  if (request.op === "apply") args.push("--plan", request.plan);
  if ("job" in request) args.push("--job", request.job);
  return [...args, "--root", root, "--json"];
}
/** The renderer supplies typed operations; only this module chooses executable arguments and state root. */
export function runOnboarding(request: OnboardingRequest, root: string, bin = xmeshBinary()): Promise<DataResult<unknown>> {
  return new Promise((resolve) => {
    const child = execFile(bin, onboardingArgs(request, root), { encoding: "utf8", timeout: 30_000, maxBuffer: 1024 * 1024, windowsHide: true }, (error, stdout) => {
      try {
        const result = JSON.parse(stdout) as DataResult<unknown>;
        if (!result || typeof result !== "object" || typeof result.ok !== "boolean" || result.ok && !("data" in result) || !result.ok && (!result.error || typeof result.error.message !== "string")) throw new Error("invalid response");
        resolve(JSON.parse(redactOnboarding(JSON.stringify(result))) as DataResult<unknown>);
      } catch {
        resolve({ ok: false, error: { code: (error as NodeJS.ErrnoException | null)?.code === "ENOENT" ? "binary_missing" : "setup_failed", message: (error as NodeJS.ErrnoException | null)?.code === "ENOENT" ? "找不到 xmesh 内核，请安装或更新应用。" : "无法读取设置任务状态。请重试或运行诊断。", next_action: "检查内核版本与状态目录后重试。" } });
      }
    });
    child.stdin?.on("error", () => undefined);
    child.stdin?.end(request.op === "plan" ? JSON.stringify(request.input) : request.op === "set_password" ? JSON.stringify({ password: request.password }) : undefined);
  });
}
export function createOnboardingService(root: string, open: (url: string) => Promise<unknown>, run: typeof runOnboarding = runOnboarding, copy?: (text: string) => void): (request: OnboardingRequest) => Promise<DataResult<unknown>> {
  return async (request) => {
    if (request.op === "resume" && request.open_auth) {
      const status = await run({ op: "status", job: request.job }, root);
      if (!status.ok) return status;
      const job = status.data as OnboardingJob;
      if (!job.auth_url || !isLoginUrl(job.auth_url, job.control_url || "https://controlplane.tailscale.com")) return { ok: false, error: { code: "invalid_auth_url", message: "授权地址与此任务的控制面不匹配。", next_action: "检查控制面设置后重试。" } };
      try { await open(job.auth_url); } catch { return { ok: false, error: { code: "browser_failed", message: "无法打开系统浏览器。", next_action: "检查默认浏览器后重试。" } }; }
    }
    const result = await run(request, root);
    if (result.ok && copy) {
      if (request.op === "doctor" && request.copy) copy(onboardingDiagnostics(result.data));
      if (request.op === "status" && request.copy_prompt) {
        const prompt = verificationPrompt(result.data as OnboardingJob);
        if (prompt) copy(redactOnboarding(prompt));
      }
    }
    return result;
  };
}

/** Deterministic typed fixture for the Electron test harness. */
export function fakeOnboarding(root: string, state: { cliFails: boolean; setupWaiting?: boolean; setupConflict?: boolean }, calls: { op: string; arg?: unknown }[]): (request: OnboardingRequest) => Promise<DataResult<unknown>> {
  const plans = new Map<string, OnboardingPlan>();
  const jobs = new Map<string, OnboardingJob>();
  let sequence = 0;
  const now = (): string => new Date().toISOString();
  return async (request) => {
    calls.push({ op: "onboarding", arg: request.op === "set_password" ? { op: request.op, job: request.job, password: "[redacted]" } : request });
    if (request.op === "inspect") return { ok: true, data: { schema_version: 1, platform: process.platform, arch: process.arch, hostname: "my-computer", suggested_node_name: "my-computer", root, clients: [{ id: "codex", path: "/home/me/.codex/config.toml", installed: true, version: "1.0" }, { id: "claude-code", path: "/home/me/.claude.json", installed: false, version: "" }], nodes: [], jobs: [...jobs.values()] } satisfies OnboardingInspect };
    if (request.op === "doctor") return { ok: true, data: { ready: true, message: "Kernel and setup storage are available" } };
    if (request.op === "plan") {
      const input: OnboardingInput = request.input;
      const plan: OnboardingPlan = { schema_version: 1, id: `plan-${++sequence}`, input, config_path: `${root}/${input.node_name}/xmesh.yaml`, config: JSON.stringify({ schema_version: 2, node: { name: input.node_name, roles: ["agent", "gateway"] }, mesh: { lan: { enabled: input.network.mode !== "tailnet", mdns: input.purpose !== "local" }, tailnet: { enabled: input.network.mode !== "lan", control_url: input.network.control_url, auth_method: input.network.auth_method, authkey_file: input.network.authkey_file } }, agent: { default_cwd: input.workspace, cwd_policy: { allow: [input.workspace] } } }, null, 2), before_hash: "", endpoint: "http://127.0.0.1:8080/mcp", summary: ["创建节点配置与工作目录策略", "安装并启动用户服务", "验证 MCP 工具调用"], client_changes: input.clients.map((id) => ({ id, path: id === "codex" ? "/home/me/.codex/config.toml" : "/home/me/.claude.json", before_hash: "", exists: true, conflict: state.setupConflict === true, summary: "添加 xmesh MCP 配置并备份原文件" })), steps: [], created_at: now() };
      plans.set(plan.id, plan); return { ok: true, data: plan };
    }
    if (request.op === "apply") {
      const plan = plans.get(request.plan); if (!plan) return { ok: false, error: { code: "not_found", message: "Plan not found" } };
      const job: OnboardingJob = { schema_version: 1, id: `job-${++sequence}`, plan_id: plan.id, node_name: plan.input.node_name, purpose: plan.input.purpose, status: "running", next_action: "", verification_tool: "local/fs.stat", verification_path: "/home/me/work/.xmesh-verify-test", steps: [{ id: "install", status: "running", message: "安装用户服务" }], readiness: { installed: false, service_ready: false, client_verified: false }, created_at: now(), updated_at: now() };
      jobs.set(job.id, job); return { ok: true, data: job };
    }
    const job = jobs.get(request.job); if (!job) return { ok: false, error: { code: "not_found", message: "Job not found" } };
    const plan = plans.get(job.plan_id)!;
    if (request.op === "set_password") job.ingress_password_set = true;
    if (state.cliFails) { job.status = "failed"; job.next_action = "重试安装用户服务。"; job.steps = [{ id: "install", status: "failed", message: "服务安装失败", code: "install_failed", next_action: "检查服务权限后重试。" }]; }
    else if (state.setupWaiting) { job.status = "waiting_user"; job.next_action = "请在浏览器授权并批准此设备。"; job.auth_url = "https://login.tailscale.com/a/test"; job.steps = [{ id: "login", status: "waiting_user", message: "等待浏览器授权或设备批准" }]; }
    else if (job.purpose === "cloud" && !job.ingress_password_set) { job.status = "waiting_user"; job.readiness = { installed: true, service_ready: true, client_verified: false }; job.next_action = "请设置云端入口登录密码。"; job.steps = [{ id: "verify", status: "waiting_user", code: "ingress_password_pending", message: "等待设置入口密码" }]; }
    else { job.readiness = { installed: true, service_ready: true, client_verified: request.op === "verify" && plan.input.clients.length > 0 }; job.status = job.readiness.client_verified ? "succeeded" : "waiting_user"; job.next_action = job.readiness.client_verified ? "" : "请在所选客户端调用 xmesh 工具，然后点击检查调用。"; job.steps = [{ id: "install", status: "succeeded", message: "用户服务已启动" }, { id: "client", status: job.readiness.client_verified ? "succeeded" : "waiting_user", message: "验证客户端实际调用" }]; }
    job.updated_at = now(); return { ok: true, data: job };
  };
}
