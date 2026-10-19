import type { ChildProcess } from "node:child_process";

/** Wait for the owned native process, independently of descendant stdio handles. */
export function waitForNativeExit(child: ChildProcess, timeoutMs = 10_000): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const cleanup = (): void => {
      clearTimeout(timer);
      child.off("exit", exited);
      child.off("error", failed);
    };
    const exited = (): void => { cleanup(); resolve(); };
    const failed = (error: Error): void => { cleanup(); reject(error); };
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error(`Electron process ${child.pid ?? "unknown"} did not exit within ${timeoutMs}ms`));
    }, timeoutMs);
    child.once("exit", exited);
    child.once("error", failed);
  });
}

/** Reclaim only this fixture's process if the automation exit request stalls. */
export async function stopNativeTestProcess(
  child: ChildProcess,
  requestExit: () => Promise<unknown>,
  gracefulMs = 2_000,
  forceMs = 5_000,
): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = waitForNativeExit(child, forceMs + 5_000);
  const stop = (signal: NodeJS.Signals): void => {
    if (child.exitCode === null && child.signalCode === null) child.kill(signal);
  };
  const gentle = setTimeout(() => stop("SIGTERM"), gracefulMs);
  const force = setTimeout(() => stop("SIGKILL"), forceMs);
  // A destroyed automation context can leave this request pending. Native exit
  // is the completion signal for fixture cleanup.
  void Promise.resolve().then(requestExit).catch(() => undefined);
  try { await exited; }
  finally { clearTimeout(gentle); clearTimeout(force); }
}
