import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { expect } from "@playwright/test";
import { Driver } from "./driver.js";
import { failure } from "./errors.js";
import type { WorkerInit, Step } from "./types.js";

let driver: Driver | undefined;
let activeRun: string | undefined;
let eventCount = 0;
const send = (value: unknown) => {
  if (process.connected) process.send?.(value);
};
const emit = (event: Record<string, unknown>) => {
  if (event.kind === "process") {
    send({ type: "process", ...event });
    return;
  }
  if (!activeRun || eventCount++ >= 1000) return;
  send({
    type: "event",
    run_id: activeRun,
    event: { ...event, time: new Date().toISOString() },
  });
};
let closing = false;
async function close(): Promise<void> {
  if (closing) return;
  closing = true;
  const timer = setTimeout(() => process.exit(1), 8000);
  timer.unref();
  try {
    await driver?.close();
  } finally {
    process.exit(0);
  }
}
process.on("disconnect", () => void close());
process.on("SIGTERM", () => void close());
process.on("SIGINT", () => void close());
process.on("message", async (raw: unknown) => {
  const m = raw as { id: string; op: string; args: any };
  try {
    let value: unknown;
    if (m.op === "open") {
      driver = new Driver(m.args as WorkerInit, emit);
      value = await driver.open();
    } else if (m.op === "snapshot")
      value = await driver!.snapshot(
        m.args.page_id,
        m.args.target,
        m.args.max_length,
      );
    else if (m.op === "close") {
      await driver?.close();
      driver = undefined;
      value = { closed: true };
    } else if (m.op === "run") {
      const { run_id, dir, steps, code, params } = m.args as {
        run_id: string;
        dir: string;
        steps?: Step[];
        code?: string;
        params?: Record<string, unknown>;
      };
      activeRun = run_id;
      eventCount = 0;
      driver!.beginRun();
      let tracing = false;
      try {
        if (driver!.init.environment.trace) {
          await driver!.context.tracing.start({
            screenshots: true,
            snapshots: true,
            sources: false,
          });
          tracing = true;
        }
        const step = async (id: string, action: () => Promise<unknown>) => {
          const started = Date.now();
          emit({ kind: "step", id, status: "started" });
          try {
            const out = await action();
            if (out && typeof out === "object" && "artifact" in out)
              send({
                type: "artifact",
                run_id,
                artifact: (out as { artifact: unknown }).artifact,
              });
            else if (out !== undefined)
              emit({ kind: "output", id, value: out });
            emit({
              kind: "step",
              id,
              status: "passed",
              duration_ms: Date.now() - started,
            });
          } catch (e) {
            emit({
              kind: "step",
              id,
              status: "failed",
              duration_ms: Date.now() - started,
            });
            throw e;
          }
        };
        if (steps)
          for (const s of steps) await step(s.id, () => driver!.step(s, dir));
        else {
          const file = path.join(dir, "script.mjs");
          fs.writeFileSync(file, code!, { mode: 0o600 });
          try {
            const module = await import(pathToFileURL(file).href);
            if (typeof module.default !== "function")
              throw new Error("script must export a default async function");
            await module.default({
              page: driver!.page,
              context: driver!.context,
              electronApp: driver!.electronApp,
              expect,
              params: params ?? {},
              step,
            });
          } finally {
            fs.rmSync(file, { force: true });
          }
        }
        value = { passed: true };
      } catch (e) {
        try {
          send({
            type: "artifact",
            run_id,
            artifact: await driver!.screenshot(dir, "failure.png"),
          });
        } catch {
          emit({ kind: "evidence_incomplete", reason: "screenshot_failed" });
        }
        throw e;
      } finally {
        if (tracing) {
          try {
            const file = path.join(dir, "trace.zip");
            await driver!.context.tracing.stop({ path: file });
            send({
              type: "artifact",
              run_id,
              artifact: { file, name: "trace.zip", mime: "application/zip" },
            });
          } catch {
            emit({ kind: "evidence_incomplete", reason: "trace_failed" });
          }
        }
        activeRun = undefined;
      }
    }
    send({ type: "reply", id: m.id, value });
  } catch (e) {
    send({ type: "reply", id: m.id, error: failure(e) });
  }
});
