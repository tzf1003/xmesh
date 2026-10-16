import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";

/** The only `xmesh` subcommands the UI ever runs. K1 adds them alongside `--json` output. */
export const CLI_COMMANDS = ["restart", "stop"] as const;
export type CliCommand = (typeof CLI_COMMANDS)[number];

/** Where the installer puts the kernel it ships with: `<resources>/bin/xmesh[.exe]` (electron-builder extraResources). */
export function bundledXmesh(resourcesPath: string | undefined, platform: NodeJS.Platform = process.platform): string | undefined {
  if (!resourcesPath) return undefined;
  const file = path.join(resourcesPath, "bin", platform === "win32" ? "xmesh.exe" : "xmesh");
  return fs.existsSync(file) ? file : undefined;
}

/** `XMESH_BIN` wins, then the kernel bundled with the installed app, then `xmesh` on PATH. */
export function xmeshBinary(
  env: NodeJS.ProcessEnv = process.env,
  resourcesPath: string | undefined = (process as { resourcesPath?: string }).resourcesPath,
  platform: NodeJS.Platform = process.platform,
): string {
  return env.XMESH_BIN || bundledXmesh(resourcesPath, platform) || "xmesh";
}

export class CliError extends Error {
  constructor(
    readonly command: string,
    message: string,
  ) {
    super(message);
    this.name = "CliError";
  }
}

/** Runs `xmesh <command> --json` without a shell and resolves on exit code 0. */
export function runXmesh(command: CliCommand, bin: string = xmeshBinary()): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(bin, [command, "--json"], { timeout: 120_000, windowsHide: true }, (err, stdout, stderr) => {
      if (err) reject(new CliError(command, String(stderr || err.message).trim()));
      else resolve(stdout);
    });
  });
}
