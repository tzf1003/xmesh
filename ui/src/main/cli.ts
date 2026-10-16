import { execFile } from "node:child_process";

/** The only `xmesh` subcommands the UI ever runs. K1 adds them alongside `--json` output. */
export const CLI_COMMANDS = ["restart", "stop"] as const;
export type CliCommand = (typeof CLI_COMMANDS)[number];

export function xmeshBinary(env: NodeJS.ProcessEnv = process.env): string {
  return env.XMESH_BIN || "xmesh";
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

/** First-run subcommands. Unlike `CliCommand` these take arguments, built here from validated values only. */
export type SetupCliArgs = readonly ["install", "--config", string] | readonly ["start", "--config", string];

/**
 * Runs `xmesh install|start --config <file>` without a shell and returns a
 * single line for people: the first line of stdout on success, of stderr on
 * failure. A missing binary is reported as such.
 */
export function runXmeshSetup(args: SetupCliArgs, bin: string = xmeshBinary()): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(bin, [...args], { timeout: 120_000, windowsHide: true }, (err, stdout, stderr) => {
      const line = (s: string): string => s.trim().split(/\r?\n/)[0]?.slice(0, 300) ?? "";
      if (!err) return resolve(line(stdout));
      const missing = (err as NodeJS.ErrnoException).code === "ENOENT";
      reject(new CliError(args[0], missing ? `找不到 xmesh 可执行文件：${bin}` : line(String(stderr)) || err.message));
    });
  });
}
