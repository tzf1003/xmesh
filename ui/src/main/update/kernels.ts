import { ControlClient } from "../client";
import { type CliCommand, type CliRun } from "../cli";
import { type Instance, kernelGone } from "../discovery";
import { sameVersion } from "../../shared/version";
import { VersionMismatchError } from "../../shared/control";
import type { StopResult } from "./updater";

interface KernelServices {
  discover(): Instance[];
  run(command: CliCommand, node: string): Promise<CliRun>;
	/** Persist the running service's configuration before its control channel exits. */
	beforeStop?(node: string, instance: Instance): Promise<void>;
	/** Converts stopped-node configuration with the newly installed kernel before starting it. */
	prepare?(node:string):Promise<void>;
  /** When set, starting a kernel must also prove it runs this bundled version. */
  version?: string;
  gone?: (pid: number,startedAt?:string) => boolean;
  status?: (instance: Instance) => Promise<{ control: { kernel_version: string } }>;
  timeoutMs?: number;
  pollMs?: number;
}

/** Update service commands are complete only after observing the process and its control version. */
export function kernelServices(env: KernelServices): { stop(node: string): Promise<StopResult>; start(node: string): Promise<void>; refresh(node: string): Promise<void> } {
  const timeout = env.timeoutMs ?? 30_000;
  const pause = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, env.pollMs ?? 100));
  const status = (instance: Instance): Promise<{ control: { kernel_version: string } }> =>
    env.status?.(instance) ?? new ControlClient({ endpoint: instance.manifest.endpoint, requestTimeoutMs: Math.min(timeout, 2000) }).getStatus();
  const services = {
    async stop(node: string): Promise<StopResult> {
      const instance = env.discover().find((i) => i.id === node);
      if (!instance || (env.gone ?? kernelGone)(instance.manifest.pid,instance.manifest.started_at)) return "absent";
      await env.beforeStop?.(node, instance);
      const result = await env.run("stop", node);
      if (result.code !== 0) throw new Error(`无法停止内核 ${node}: ${result.stderr || result.stdout}`);
      const deadline = Date.now() + timeout;
      do {
        if ((env.gone ?? kernelGone)(instance.manifest.pid,instance.manifest.started_at)) return "stopped";
        await pause();
      } while (Date.now() < deadline);
      throw new Error(`内核 ${node} 的旧进程仍在运行，更新尚未开始`);
    },
    async start(node: string): Promise<void> {
		await env.prepare?.(node);
      const result = await env.run("start", node);
      if (result.code !== 0) throw new Error(`无法启动内核 ${node}: ${result.stderr || result.stdout}`);
      const deadline = Date.now() + timeout;
      let version = "";
      do {
        const instances = env.discover();
        const instance = instances.find((i) => i.id === node) ?? instances.find((i) => i.manifest.node === node);
        if (instance) {
          try {
            version = (await status(instance)).control.kernel_version;
            if (!env.version || sameVersion(version, env.version)) return;
          } catch {
            // The service can publish its manifest before the control channel starts answering.
          }
        }
        await pause();
      } while (Date.now() < deadline);
      throw new Error(`内核 ${node} 启动验收失败${version ? `：运行版本 ${version}，预期 ${env.version}` : "：控制通道未就绪"}`);
    },
    async refresh(node: string): Promise<void> {
      const instance = env.discover().find((i) => i.id === node);
      if (!env.version || !instance || (env.gone ?? kernelGone)(instance.manifest.pid,instance.manifest.started_at)) return;
      try {
        if (sameVersion((await status(instance)).control.kernel_version, env.version)) return;
      } catch (error) {
        // A previous control protocol can still be stopped through its installed service.
        if (!(error instanceof VersionMismatchError)) throw error;
      }
      await services.stop(node);
      await services.start(node);
    },
  };
  return services;
}
