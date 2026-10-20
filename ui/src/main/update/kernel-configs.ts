import * as fs from "node:fs";
import * as path from "node:path";
import { ControlClient } from "../client";
import type { Instance } from "../discovery";
import type { SetupCliArgs } from "../cli";

interface ConfigPaths { configFile: string; stateDir: string }
function valid(value: unknown): value is ConfigPaths {
  const p = value as Partial<ConfigPaths> | null;
  return typeof p?.configFile === "string" && path.isAbsolute(p.configFile) &&
    typeof p.stateDir === "string" && path.isAbsolute(p.stateDir);
}

/** Retains actual service paths across replacement of the desktop process. */
export class KernelConfigs {
  constructor(private readonly file: string,
    private readonly run: (args: SetupCliArgs) => Promise<string>,
    private readonly readPaths = (instance: Instance): Promise<ConfigPaths> =>
      new ControlClient({ endpoint: instance.manifest.endpoint }).getUpgradePaths()) {}

  async capture(node: string, instance: Instance): Promise<void> {
    if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/.test(node)) throw new Error("Invalid node name");
    const paths = await this.readPaths(instance);
    if (!valid(paths)) throw new Error("内核配置和状态目录必须使用绝对路径");
    const saved = this.read();
    saved[node] = paths;
    fs.mkdirSync(path.dirname(this.file), { recursive: true, mode: 0o700 });
    const temporary = `${this.file}.tmp`;
    fs.writeFileSync(temporary, JSON.stringify(saved), { mode: 0o600 });
    fs.renameSync(temporary, this.file);
  }

  async prepare(node: string): Promise<void> {
    if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/.test(node)) throw new Error("Invalid node name");
    const paths = this.read()[node];
    await this.run(paths
      ? ["migrate", "--config", paths.configFile, "--state-dir", paths.stateDir]
      : ["migrate", "--name", `xmesh-${node}`]);
  }

  private read(): Record<string, ConfigPaths> {
    let raw: unknown;
    try { raw = JSON.parse(fs.readFileSync(this.file, "utf8")); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return {};
      throw error;
    }
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) throw new Error("无效的内核升级配置记录");
    const entries = Object.entries(raw);
    if (entries.some(([, value]) => !valid(value))) throw new Error("无效的内核升级配置路径");
    return Object.fromEntries(entries) as Record<string, ConfigPaths>;
  }
}
