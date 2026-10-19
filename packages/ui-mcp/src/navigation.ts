import fs from "node:fs";
import path from "node:path";
import type { BrowserServer } from "@playwright/test";
import { UIError } from "./errors.js";

// Chromium pauses page and frame targets at creation, so the first document and
// every redirect are checked before transmission. The browser is dedicated to
// this session; its debugging endpoint binds to loopback on an ephemeral port.
export async function navigationGuard(
  server: BrowserServer,
  origins: string[],
  denied: (origin: string) => void,
): Promise<() => void> {
  const profile = server
    .process()
    .spawnargs.find((arg) => arg.startsWith("--user-data-dir="))
    ?.slice("--user-data-dir=".length);
  if (!profile)
    throw new UIError(
      "preflight_failed",
      "Chromium profile is unavailable for navigation supervision",
    );
  const [port, endpoint] = fs
    .readFileSync(path.join(profile, "DevToolsActivePort"), "utf8")
    .trim()
    .split("\n");
  if (!/^\d+$/.test(port ?? "") || !endpoint?.startsWith("/devtools/browser/"))
    throw new UIError(
      "preflight_failed",
      "Chromium navigation endpoint is invalid",
    );
  const socket = new WebSocket(`ws://127.0.0.1:${port}${endpoint}`);
  let closing = false,
    sequence = 0;
  const sessions = new Set<string>();
  const pending = new Map<
    number,
    {
      resolve(value: any): void;
      reject(error: unknown): void;
      timer: NodeJS.Timeout;
    }
  >();
  const fail = () => {
    for (const item of pending.values()) {
      clearTimeout(item.timer);
      item.reject(
        new UIError("application_exited", "navigation supervisor disconnected"),
      );
    }
    pending.clear();
    if (!closing) void server.close().catch(() => {});
  };
  socket.addEventListener("close", fail);
  socket.addEventListener("error", fail);
  const send = (
    method: string,
    params: Record<string, unknown> = {},
    sessionId?: string,
  ): Promise<any> =>
    new Promise((resolve, reject) => {
      const id = ++sequence;
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new UIError("timeout", "navigation supervision timed out"));
      }, 10000);
      timer.unref();
      pending.set(id, { resolve, reject, timer });
      try {
        socket.send(
          JSON.stringify({
            id,
            method,
            params,
            ...(sessionId ? { sessionId } : {}),
          }),
        );
      } catch (error) {
        clearTimeout(timer);
        pending.delete(id);
        reject(error);
      }
    });
  const attach = {
    autoAttach: true,
    waitForDebuggerOnStart: true,
    flatten: true,
    filter: [{ type: "page" }, { type: "iframe" }],
  };
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data));
    if (message.id) {
      const item = pending.get(message.id);
      if (item) {
        pending.delete(message.id);
        clearTimeout(item.timer);
        message.error
          ? item.reject(new Error(message.error.message))
          : item.resolve(message.result);
      }
      return;
    }
    if (message.method === "Target.detachedFromTarget")
      sessions.delete(message.params.sessionId);
    if (message.method === "Target.attachedToTarget") {
      const sessionId = message.params.sessionId;
      sessions.add(sessionId);
      void (async () => {
        await send(
          "Fetch.enable",
          { patterns: [{ resourceType: "Document", requestStage: "Request" }] },
          sessionId,
        );
        await send("Target.setAutoAttach", attach, sessionId);
        await send("Runtime.runIfWaitingForDebugger", {}, sessionId);
      })().catch(() => {
        if (!closing && sessions.has(sessionId))
          void server.close().catch(() => {});
      });
    }
    if (message.method === "Fetch.requestPaused") {
      const request = message.params;
      const origin = new URL(request.request.url).origin;
      const allowed = origins.includes(origin);
      if (!allowed) denied(origin);
      void send(
        allowed ? "Fetch.continueRequest" : "Fetch.failRequest",
        {
          requestId: request.requestId,
          ...(!allowed ? { errorReason: "BlockedByClient" } : {}),
        },
        message.sessionId,
      ).catch(() => {
        if (!closing && sessions.has(message.sessionId))
          void server.close().catch(() => {});
      });
    }
  });
  try {
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(
        () =>
          reject(
            new UIError(
              "preflight_failed",
              "navigation supervisor did not connect",
            ),
          ),
        10000,
      );
      socket.addEventListener(
        "open",
        () => {
          clearTimeout(timer);
          resolve();
        },
        { once: true },
      );
      socket.addEventListener(
        "error",
        () => {
          clearTimeout(timer);
          reject(
            new UIError(
              "preflight_failed",
              "navigation supervisor connection failed",
            ),
          );
        },
        { once: true },
      );
    });
    await send("Target.setAutoAttach", attach);
  } catch (error) {
    closing = true;
    socket.close();
    throw error;
  }
  return () => {
    closing = true;
    socket.close();
  };
}
