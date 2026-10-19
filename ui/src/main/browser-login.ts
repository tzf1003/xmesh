import { spawn, type ChildProcess } from "node:child_process";
import type { Readable } from "node:stream";
import { xmeshBinary } from "./cli";

/** Only the selected control plane may ask the app to open an authentication URL. */
export function isLoginUrl(value: string, controlUrl: string): boolean {
  try {
    const u = new URL(value);
    const control = new URL(controlUrl);
    if (u.protocol !== "https:" || u.username !== "" || u.password !== "") return false;
    const official = new Set(["https://controlplane.tailscale.com", "https://login.tailscale.com"]);
    return official.has(control.origin)
      ? u.origin === "https://login.tailscale.com" && u.pathname.startsWith("/a/")
      : u.origin === control.origin;
  } catch {
    return false;
  }
}

export type LoginChild = Pick<ChildProcess, "on" | "kill"> & { stdout: Readable | null; stderr: Readable | null };

/** Streams typed progress from xmesh login, opening the browser once per challenge. */
export function runBrowserLogin(
  config: string,
  stateDir: string,
  controlUrl: string,
  open: (url: string) => Promise<unknown>,
  launch: () => LoginChild = () => {
    // A browser login uses the user's browser session, independent of CI auth variables.
    const env = { ...process.env };
    for (const name of ["TS_AUTHKEY", "TS_CLIENT_SECRET", "TS_CLIENT_ID", "TS_ID_TOKEN", "TS_AUDIENCE"]) delete env[name];
    return spawn(xmeshBinary(), ["login", `--config=${config}`, `--state-dir=${stateDir}`, "--json"], {
      windowsHide: true, stdio: ["ignore", "pipe", "pipe"], env,
    });
  },
): Promise<void> {
  return new Promise((resolve, reject) => {
    let child: LoginChild;
    try { child = launch(); } catch { reject(new Error("无法启动 xmesh 登录，请检查内核程序")); return; }
    let pending = "";
    let stderr = "";
    let ready = false;
    let failure: Error | undefined;
    const opened = new Set<string>();
    let opening = Promise.resolve<unknown>(undefined);
    const fail = (message: string): void => {
      failure ??= new Error(message);
      child.kill();
    };
    const timer = setTimeout(() => fail("浏览器授权超时，请完成登录或在控制面批准设备后重试"), 310_000);
    const line = (text: string): void => {
      if (failure) return;
      let p: { state?: unknown; url?: unknown };
      try { p = JSON.parse(text) as typeof p; } catch { fail("xmesh 返回了无效的登录状态"); return; }
      if (p === null || typeof p !== "object") { fail("xmesh 返回了无效的登录状态"); return; }
      if (p.state === "ready") ready = true;
      else if (p.state === "needs_login") {
        if (typeof p.url !== "string" || !isLoginUrl(p.url, controlUrl)) { fail("控制面返回了无效的授权地址"); return; }
        if (!opened.has(p.url)) {
          opened.add(p.url);
          const url = p.url;
          opening = opening.then(() => open(url)).catch(() => fail("无法打开浏览器，请检查系统默认浏览器后重试"));
        }
      } else if (p.state !== "waiting" && p.state !== "needs_approval") fail("xmesh 返回了未知的登录状态");
    };
    child.stdout?.setEncoding("utf8");
    child.stdout?.on("data", (chunk: string) => {
      pending += chunk;
      if (pending.length > 8192) { fail("登录状态超出长度限制"); return; }
      let end: number;
      while ((end = pending.indexOf("\n")) >= 0) {
        const text = pending.slice(0, end).trim();
        pending = pending.slice(end + 1);
        if (text) line(text);
      }
    });
    child.stderr?.setEncoding("utf8");
    child.stderr?.on("data", (chunk: string) => { stderr = (stderr + chunk).slice(-2000); });
    child.on("error", () => {
      clearTimeout(timer);
      reject(new Error("无法启动 xmesh 登录，请检查内核程序"));
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      if (pending.trim()) line(pending.trim());
      void opening.then(() => {
        if (failure) reject(failure);
        else if (code !== 0 || !ready) reject(new Error(stderr.trim() || "浏览器授权未完成，请重试"));
        else resolve();
      });
    });
  });
}
