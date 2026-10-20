import * as fs from "node:fs";
import { parse } from "yaml";

/** Bundled separately so the shipped main process needs no node_modules. */
export function kernelInstallOptions({ configFile }: { configFile: string }): string[] {
  const config = parse(fs.readFileSync(configFile, "utf8")) as { mesh?: { lan?: { enabled?: boolean }; tailnet?: { enabled?: boolean } } };
  return config?.mesh?.lan?.enabled === false && config.mesh.tailnet?.enabled !== true ? ["--local-only"] : [];
}

export function kernelConfigNode({ configFile }: { configFile: string }): string {
  const config = parse(fs.readFileSync(configFile, "utf8")) as { node?: { name?: unknown } };
  const node = config?.node?.name;
  if (typeof node !== "string" || !/^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/.test(node)) throw new Error("无效的配置节点名");
  return node;
}
