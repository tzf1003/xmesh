import * as fs from "node:fs";
import * as path from "node:path";
import { ControlClient } from "./client";
import { discoverConfigured, discoverInstances, kernelGone, protectedStateRoots, type Instance } from "./discovery";
import type { CliRun } from "./cli";
import type { DataResult } from "../shared/ipc";
import { parseInstanceId } from "../shared/ipc";
import { parseKernelRequest, type KernelNode, type KernelResult } from "../shared/kernel";
import { sameVersion } from "../shared/version";

export interface KernelPaths { configFile: string; stateDir: string; serviceNode?: string }
export interface KernelTarget extends KernelNode, KernelPaths { instance?: Instance }

/** Only discovered local nodes are addressable; the renderer never supplies paths. */
export function discoverKernelTargets(roots: string[], saved: (id: string) => KernelPaths | undefined,
  protectedRoots: readonly string[] = protectedStateRoots(), savedIds: readonly string[] = []): KernelTarget[] {
  const instances = discoverInstances(roots, protectedRoots);
  const ids = new Set([...discoverConfigured(roots), ...instances.map((i) => i.id), ...savedIds]);
  for (const root of roots) {
    try {
      for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
        if (entry.isDirectory() && parseInstanceId(entry.name) && fs.existsSync(path.join(root, entry.name, "xmesh.yaml"))) ids.add(entry.name);
      }
    } catch { /* unreadable roots have no manageable nodes */ }
  }
  return [...ids].sort().flatMap((id): KernelTarget[] => {
    if (parseInstanceId(id) === undefined) return [];
    const instance = instances.find((i) => i.id === id);
    const recorded = saved(id);
    const root = roots.find((r) => fs.existsSync(path.join(r, id, instance ? "control.json" : "xmesh.yaml"))) ??
      roots.find((r) => recorded && path.resolve(r, id) === path.resolve(recorded.stateDir));
    if (!root) return [];
    const stateDir = path.resolve(root, id);
    const paths = recorded ?? { stateDir, configFile: path.join(stateDir, "xmesh.yaml") };
    return [{ id, name: instance?.manifest.node || id, running: !!instance && !kernelGone(instance.manifest.pid, instance.manifest.started_at),
      managed: !instance?.protected && !protectedRoots.includes(root), configFile: paths.configFile, stateDir: paths.stateDir,
      ...(paths.serviceNode && { serviceNode: paths.serviceNode }), instance }];
  });
}

interface Environment {
  targets(): KernelTarget[];
  resolve(target: KernelTarget): Promise<KernelPaths>;
  run(args: readonly string[]): Promise<CliRun>;
  installOptions?(paths: KernelPaths): string[];
  serviceOptions?(paths: KernelPaths): Promise<string[]>;
  installed?(node: string, paths: KernelPaths): Promise<string>;
  managed?(target: KernelTarget): Promise<boolean>;
  changed(): void;
  version?: string;
  gone?: typeof kernelGone;
  status?: (instance: Instance) => Promise<{ control: { kernel_version: string } }>;
  timeoutMs?: number;
  pollMs?: number;
}

class KernelFailure extends Error {
  constructor(readonly code: string, message: string) { super(message); }
}
function missing(result: CliRun): boolean {
  try { return JSON.parse(result.stdout)?.error?.reason === "not_installed"; } catch { return false; }
}
function cliFailure(result: CliRun): KernelFailure {
  try {
    const error = JSON.parse(result.stdout)?.error;
    if (typeof error?.message === "string") return new KernelFailure(error.error || "cli_failed", error.message);
  } catch { /* migrate has human-readable output */ }
  return new KernelFailure("cli_failed", (result.stderr || result.stdout || "内核操作失败").trim().slice(0, 1000));
}

/** Service registration changes retain the node's configuration, credentials and device state. */
export class KernelLifecycle {
  private readonly pending = new Set<string>();
  constructor(private readonly env: Environment) {}

  private async nodes(): Promise<KernelNode[]> {
    return Promise.all(this.env.targets().map(async (target) => {
      const { id, name, running } = target;
      return { id, name, running, managed: await this.managed(target) };
    }));
  }

  private async managed(target: KernelTarget): Promise<boolean> {
    try { return await this.env.managed?.(target) ?? target.managed; }
    catch { return false; }
  }

  async request(raw: unknown): Promise<DataResult<KernelResult>> {
    const req = parseKernelRequest(raw);
    if (!req) return { ok: false, error: { code: "invalid_args", message: "无效的内核操作" } };
    if (req.op === "list") {
      try { return { ok: true, data: { nodes: await this.nodes() } }; }
      catch (e) { return { ok: false, error: { code: "internal", message: e instanceof Error ? e.message : String(e) } }; }
    }
    if (this.pending.has(req.node)) return { ok: false, error: { code: "busy", message: "该节点正在执行内核操作，请稍候" } };
    this.pending.add(req.node);
    try {
      const target = this.env.targets().find((n) => n.id === req.node);
      if (!target) throw new KernelFailure("not_found", "未找到该本机节点，请先使用开始使用向导创建配置");
      if (!await this.managed(target)) throw new KernelFailure("forbidden", "系统级内核或无法确认服务权限的节点需要由管理员管理");
      const paths = await this.env.resolve(target);
      paths.serviceNode ??= target.running ? target.instance?.manifest.node : undefined;
      let retainedOptions: string[] = [];
      if (!path.isAbsolute(paths.configFile) || !path.isAbsolute(paths.stateDir)) throw new KernelFailure("invalid_path", "内核配置和状态目录必须为绝对路径");
      const command = async (op: string, allowMissing = false): Promise<void> => {
        const args = [op, "--config", paths.configFile];
        if (op !== "install" && op !== "migrate" && paths.serviceNode) args.push("--name", `xmesh-${paths.serviceNode}`);
        if (op === "install" || op === "migrate") args.push("--state-dir", paths.stateDir);
        if (op === "install") args.push(...(this.env.installOptions?.(paths) ?? []), ...retainedOptions);
        if (op !== "migrate") args.push("--json");
        const result = await this.env.run(args);
        if (result.code !== 0 && !(allowMissing && missing(result))) throw cliFailure(result);
        if (op === "install" && this.env.installed) paths.serviceNode = await this.env.installed(target.id, paths);
      };
      const stop = async (): Promise<void> => {
        const wasRunning = target.running;
        const previous = target.instance;
        if (!wasRunning) return;
        await command("stop", !wasRunning);
        if (previous && wasRunning) {
          await this.wait(() => (this.env.gone ?? kernelGone)(previous.manifest.pid, previous.manifest.started_at), "旧内核进程尚未退出，操作已停止");
        }
      };
      const start = async (): Promise<void> => {
        await command("migrate");
        await command("start");
        await this.wait(async () => {
          const instance = this.env.targets().find((n) => n.id === target.id)?.instance;
          if (!instance) return false;
          try {
            const status = await (this.env.status?.(instance) ?? new ControlClient({ endpoint: instance.manifest.endpoint, requestTimeoutMs: 2000 }).getStatus());
            return !this.env.version || sameVersion(status.control.kernel_version, this.env.version);
          } catch { return false; }
        }, "内核启动验收失败：控制通道未就绪或运行版本不匹配");
      };
      switch (req.op) {
        case "install":
          if (target.running) throw new KernelFailure("conflict", "请先关闭内核，或选择内核重装");
          await command("migrate");
          await command("install");
          break;
        case "uninstall":
          await stop();
          await command("uninstall", true);
          break;
        case "reinstall":
          retainedOptions = await this.env.serviceOptions?.(paths) ?? [];
          await stop();
          await command("migrate");
          await command("uninstall", true);
          await command("install");
          await start();
          break;
        case "start": await start(); break;
        case "stop": await stop(); break;
        case "restart": await stop(); await start(); break;
      }
      this.env.changed();
      return { ok: true, data: { nodes: await this.nodes() } };
    } catch (e) {
      this.env.changed();
      return { ok: false, error: { code: e instanceof KernelFailure ? e.code : "internal", message: e instanceof Error ? e.message : String(e) } };
    } finally { this.pending.delete(req.node); }
  }

  private async wait(check: () => boolean | Promise<boolean>, message: string): Promise<void> {
    const deadline = Date.now() + (this.env.timeoutMs ?? 30_000);
    do {
      if (await check()) return;
      await new Promise((resolve) => setTimeout(resolve, this.env.pollMs ?? 100));
    } while (Date.now() < deadline);
    throw new KernelFailure("timeout", message);
  }
}
