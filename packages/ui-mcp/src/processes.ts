import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { processIdentity } from "./locks.js";
import { UIError } from "./errors.js";
const exec = promisify(execFile);
export interface OwnedProcess {
  pid: number;
  birth: string;
  group: boolean;
}
export function ownProcess(pid: number, group: boolean): OwnedProcess {
  return { pid, birth: processIdentity(pid), group };
}
export async function terminateProcesses(
  processes: OwnedProcess[],
): Promise<void> {
  for (const p of [...processes].reverse()) {
    if (!p.birth || processIdentity(p.pid) !== p.birth) continue;
    try {
      if (process.platform === "win32")
        await exec("taskkill.exe", ["/PID", String(p.pid), "/T", "/F"], {
          timeout: 3000,
          windowsHide: true,
        });
      else process.kill(p.group ? -p.pid : p.pid, "SIGKILL");
    } catch (error) {
      if (
        (error as NodeJS.ErrnoException).code !== "ESRCH" &&
        processIdentity(p.pid) === p.birth
      )
        throw new UIError(
          "cleanup_pending",
          "owned browser process could not be terminated",
        );
    }
    const deadline = Date.now() + 3000;
    while (processIdentity(p.pid) === p.birth) {
      if (Date.now() >= deadline)
        throw new UIError(
          "cleanup_pending",
          "owned browser process termination has not been confirmed",
        );
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
}
