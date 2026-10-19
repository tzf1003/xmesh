import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { xmeshBinary } from "./cli";
import { type Instance, kernelGone } from "./discovery";
import { ControlClient } from "./client";
import { sameVersion } from "../shared/version";
import type { Snapshot } from "../shared/state";
import type { StopResult } from "./update/updater";

export interface ComputerHostStatus { running: boolean; pid: number; version: string; native_pid?: number }
export type ComputerHostCommand = "status" | "start" | "stop";

/** Emergency stop reaches the user Host independently of kernel health. */
export function emergencyStopComputerHost(binary: string = xmeshBinary(), endpoint?: string): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(binary, ["computer", "emergency-stop", ...(endpoint ? ["--endpoint", endpoint] : [])], { timeout: 6000, windowsHide: true, maxBuffer: 64 * 1024 }, (error) => {
      if (error) reject(new Error("本机桌面共享紧急停止失败"));
      else resolve();
    });
  });
}

/** Retain each selected node's effective endpoint through kernel outages. */
export class ComputerEmergencyTargets {
  private readonly endpoints = new Map<string, string>();
  observe(snapshot: Snapshot): void {
    if (snapshot.connection.kind !== "connected" || !snapshot.selectedInstance) return;
    const endpoint = snapshot.connection.status.paths.computer_endpoint;
    if (endpoint) this.endpoints.set(snapshot.selectedInstance, endpoint);
    else this.endpoints.delete(snapshot.selectedInstance);
  }
  endpoint(snapshot: Snapshot): string | undefined {
    this.observe(snapshot);
    if (!snapshot.selectedInstance) return undefined;
    const endpoint = this.endpoints.get(snapshot.selectedInstance);
    if (!endpoint) throw new Error("所选内核尚未提供桌面服务端点，请使用本机 Computer CLI 紧急停止");
    return endpoint;
  }
}

/** Lifecycle commands use a fixed program and argument list, never renderer data. */
export function runComputerHost(command: ComputerHostCommand, binary: string = xmeshBinary(), endpoint?: string): Promise<ComputerHostStatus | void> {
  return new Promise((resolve, reject) => {
    // Lifecycle commands address the installed per-user service. Status probes
    // must use that service's effective socket, including custom state paths.
    execFile(binary, ["computer", command, ...(command === "status" ? (endpoint ? ["--endpoint", endpoint] : []) : ["--json"])], { timeout: 30000, windowsHide: true, maxBuffer: 64 * 1024 }, (error, stdout, stderr) => {
      try {
        const result = JSON.parse(stdout) as Partial<ComputerHostStatus> & { ok?: boolean; error?: { message?: string } };
        if (error) throw new Error(result.error?.message || stderr.trim() || "桌面服务操作失败");
        if (command === "status") {
          if (typeof result.running !== "boolean" || !Number.isSafeInteger(result.pid) || typeof result.version !== "string" || (result.running && result.pid! <= 0) || (result.native_pid !== undefined && (!Number.isSafeInteger(result.native_pid) || result.native_pid < 0))) throw new Error("桌面服务状态无效");
          resolve(result as ComputerHostStatus);
        } else if (result.ok === true) resolve();
        else throw new Error(result.error?.message ?? "桌面服务操作失败");
      } catch (e) { reject(e); }
    });
  });
}

export function resumeComputerHost(binary: string = xmeshBinary(), endpoint?: string): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(binary, ["computer", "resume", ...(endpoint ? ["--endpoint", endpoint] : [])], { timeout: 6000, windowsHide: true, maxBuffer: 64 * 1024 }, (error) => {
      if (error) reject(new Error("本机桌面共享恢复失败"));
      else resolve();
    });
  });
}

interface HostServicesEnv {
  run(command: ComputerHostCommand): Promise<ComputerHostStatus | void>;
  version: string;
  gone?: (pid: number) => boolean;
  timeoutMs?: number;
  pollMs?: number;
  beforeStop?(): Promise<void>;
}

export function computerHostServices(env: HostServicesEnv): { stop(): Promise<StopResult>; start(): Promise<void> } {
  const timeout = env.timeoutMs ?? 30000;
  const pause = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, env.pollMs ?? 100));
  const status = async (): Promise<ComputerHostStatus> => {
    const value = await env.run("status");
    if (!value) throw new Error("桌面服务状态无效");
    return value;
  };
  return {
    async stop() {
      await env.beforeStop?.();
      const current = await status();
      if (!current.running) return "absent";
      await env.run("stop");
      const deadline = Date.now() + timeout;
      do {
        const gone = env.gone ?? kernelGone;
        if (gone(current.pid) && (!current.native_pid || gone(current.native_pid)) && !(await status()).running) return "stopped";
        await pause();
      } while (Date.now() < deadline);
      throw new Error("旧桌面服务仍在运行，更新尚未开始");
    },
    async start() {
      await env.run("start");
      const deadline = Date.now() + timeout;
      do {
        try {
          const current = await status();
          if (current.running && sameVersion(current.version, env.version)) return;
        } catch { /* The service may still be opening its protected endpoint. */ }
        await pause();
      } while (Date.now() < deadline);
      throw new Error("桌面服务启动验收失败：运行版本或本机通道未就绪");
    },
  };
}

/** Retains the effective Host endpoint while kernels and the desktop app restart. */
export class ComputerUpdateTarget {
  constructor(private readonly file: string, private readonly env: {
    discover(): Instance[];
    readEndpoint?(instance: Instance): Promise<string | undefined>;
    gone?(pid: number): boolean;
  }) {}

  async capture(): Promise<void> {
    const endpoints = new Set<string>();
    for (const instance of this.env.discover()) {
      if ((this.env.gone ?? kernelGone)(instance.manifest.pid)) continue;
      const endpoint = await (this.env.readEndpoint?.(instance) ??
        new ControlClient({ endpoint: instance.manifest.endpoint }).getUpgradeComputerEndpoint());
      if (endpoint !== undefined) endpoints.add(endpoint);
    }
    if (endpoints.size > 1) throw new Error("运行中的内核使用不同桌面服务端点，请分别完成服务交接后更新");
    const endpoint = endpoints.values().next().value ?? null;
    fs.mkdirSync(path.dirname(this.file), { recursive: true, mode: 0o700 });
    fs.writeFileSync(this.file + ".tmp", JSON.stringify({ endpoint }), { mode: 0o600 });
    fs.renameSync(this.file + ".tmp", this.file);
  }

  endpoint(): string | undefined {
    let value: unknown;
    try { value = JSON.parse(fs.readFileSync(this.file, "utf8")); }
    catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined; throw error; }
    const endpoint = (value as { endpoint?: unknown } | null)?.endpoint;
    if (endpoint === null) return undefined;
    if (typeof endpoint !== "string" || endpoint === "") throw new Error("无效的桌面服务升级端点记录");
    return endpoint;
  }
}

/** The renderer can request setup but cannot choose the program, arguments or OS endpoint. */
export function setupComputerHost(binary: string = xmeshBinary(), endpoint?: string): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(binary, ["computer", "setup", "--json", ...(endpoint ? ["--endpoint", endpoint] : [])], { timeout: 30000, windowsHide: true, maxBuffer: 64 * 1024 }, (error, stdout) => {
      let result: { ok?: boolean; error?: { message?: string } };
      try { result = JSON.parse(stdout) as typeof result; }
      catch { reject(new Error("桌面服务初始化未返回有效结果")); return; }
      if (error || result.ok !== true) { reject(new Error(result.error?.message ?? "桌面服务初始化失败")); return; }
      resolve();
    });
  });
}
