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
