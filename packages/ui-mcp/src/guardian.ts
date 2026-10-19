import fs from "node:fs";
import path from "node:path";
import { terminateProcesses, type OwnedProcess } from "./processes.js";
import { atomicJSON } from "./artifacts.js";
import { failure } from "./errors.js";
import { spawn, type ChildProcess } from "node:child_process";

// An independent process survives an abruptly killed controller and owns only
// the identities registered over its private IPC channel.
const processes: OwnedProcess[] = [];
const root = process.argv[2]!;
const parent = Number(process.argv[3]);
let closing = false;
let parentWaiter: ChildProcess | undefined;
process.on("message", (message: any) => {
  if (message.type === "track") processes.push(message.process as OwnedProcess);
});
async function close(): Promise<void> {
  if (closing) return;
  closing = true;
  clearInterval(watch);
  let cleanupError: ReturnType<typeof failure> | undefined;
  try {
    atomicJSON(path.join(root, "guardian.json"), {
      state: "cleaning",
      time: new Date().toISOString(),
    });
  } catch {}
  try {
    await terminateProcesses(processes);
  } catch (error) {
    cleanupError = failure(error);
  }
  try {
    const runs = path.join(root, "runs");
    if (fs.existsSync(runs))
      for (const entry of fs
        .readdirSync(runs, { withFileTypes: true })
        .filter((e) => e.isDirectory())) {
        const file = path.join(runs, entry.name, "result.json");
        try {
          const result = JSON.parse(fs.readFileSync(file, "utf8"));
          if (!result.ended_at) {
            result.status = "interrupted";
            result.ended_at = new Date().toISOString();
            result.outcome_unknown = true;
            result.evidence_incomplete = true;
            if (cleanupError) result.error = cleanupError;
            atomicJSON(file, result);
          }
        } catch {
          /* Partial journals remain available to the node administrator. */
        }
      }
  } finally {
    parentWaiter?.kill();
    try {
      atomicJSON(path.join(root, "guardian.json"), {
        state: cleanupError ? "cleanup_pending" : "closed",
        error: cleanupError,
        time: new Date().toISOString(),
      });
    } catch {}
    process.exit(cleanupError ? 1 : 0);
  }
}
const watch = setInterval(() => {
  try {
    process.kill(parent, 0);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ESRCH") void close();
  }
}, 250);
process.once("disconnect", () => void close());
if (
  process.platform === "win32" &&
  Number.isSafeInteger(parent) &&
  parent > 0
) {
  // Windows can retain inherited pipe handles after a parent dies. Wait on the
  // OS process handle as well as IPC EOF so cleanup does not depend on the pipe.
  parentWaiter = spawn(
    "powershell.exe",
    [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      `[System.Diagnostics.Process]::GetProcessById(${parent}).WaitForExit()`,
    ],
    {
      stdio: "ignore",
      windowsHide: true,
      env: {
        ...process.env,
        windir: process.env.SystemRoot ?? process.env.SYSTEMROOT,
        ComSpec:
          process.env.ComSpec ??
          path.join(
            process.env.SystemRoot ?? process.env.SYSTEMROOT ?? "C:\\Windows",
            "System32",
            "cmd.exe",
          ),
      },
    },
  );
  parentWaiter.once("exit", (code) => {
    if (code === 0) void close();
  });
  parentWaiter.once("error", () => {});
}
