// `xmesh-webui install` / `uninstall`: the web panel as its own systemd user
// service, next to the kernel's `xmesh-<node>` service. It runs as the user who
// runs the kernel (the panel finds the kernel under that user's ~/.config/xmesh).
// Everything that touches the system goes through `Deps`, so the unit text and the
// order of systemctl calls are tested without systemd.

import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { MIN_KEY_LENGTH, loadKey, validKey, writeKeyFile } from "./auth";

export const SERVICE_NAME = "xmesh-webui";

export interface UnitOptions {
  /** Absolute path of the node binary that runs the script. */
  node: string;
  /** Absolute path of xmesh-webui.js. */
  script: string;
  listen: string;
  dataDir: string;
  keyFile: string;
  tlsCert?: string;
  tlsKey?: string;
  /** The `xmesh` CLI the panel shells out to; a user service has only a short PATH. */
  xmeshBin?: string;
  lang?: string;
}

/** One word of a systemd command line: quoted when needed, with `%` and `$` doubled so systemd does not expand them. */
export function unitWord(arg: string): string {
  if (arg === "" || /[\0\r\n]/.test(arg)) throw new Error(`argument cannot be written to a unit file: ${JSON.stringify(arg)}`);
  // A function replacer: in a string replacement "$$" means one literal "$".
  const plain = arg.replaceAll("%", () => "%%").replaceAll("$", () => "$$");
  if (/^[A-Za-z0-9_@%+=:,./$-]+$/.test(plain)) return plain;
  return `"${plain.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

export function renderUnit(o: UnitOptions): string {
  for (const [name, p] of [["node", o.node], ["script", o.script], ["data dir", o.dataDir], ["key file", o.keyFile], ["TLS certificate", o.tlsCert], ["TLS key", o.tlsKey], ["xmesh binary", o.xmeshBin]] as const) {
    if (p !== undefined && !path.isAbsolute(p)) throw new Error(`${name} must be an absolute path: ${p}`);
  }
  const args = [o.node, o.script, "--listen", o.listen, "--data-dir", o.dataDir, "--key-file", o.keyFile];
  if (o.tlsCert !== undefined) args.push("--tls-cert", o.tlsCert, "--tls-key", o.tlsKey ?? "");
  if (o.lang !== undefined) args.push("--lang", o.lang);
  const lines = [
    "# Written by `xmesh-webui install`; run it again to change these settings.",
    "[Unit]",
    "Description=xmesh web management panel",
    "Documentation=https://github.com/tzf1003/xmesh",
    "After=network-online.target",
    "",
    "[Service]",
    "Type=simple",
    `ExecStart=${args.map(unitWord).join(" ")}`,
    "ExecReload=/bin/kill -HUP $MAINPID",
  ];
  if (o.xmeshBin !== undefined) lines.push(`Environment=${unitWord(`XMESH_BIN=${o.xmeshBin}`)}`);
  lines.push("Restart=on-failure", "RestartSec=3", "UMask=0077", "NoNewPrivileges=yes", "", "[Install]", "WantedBy=default.target", "");
  return lines.join("\n");
}

export interface Run {
  status: number | null;
  stdout: string;
  stderr: string;
}

export interface Deps {
  run(cmd: string, args: string[]): Run;
  env: NodeJS.ProcessEnv;
  home: string;
  uid?: number;
  out(line: string): void;
}

export function realDeps(): Deps {
  return {
    run: (cmd, args) => {
      const r = spawnSync(cmd, args, { encoding: "utf8" });
      return { status: r.error ? null : r.status, stdout: r.stdout ?? "", stderr: r.error ? String(r.error.message) : r.stderr ?? "" };
    },
    env: process.env,
    home: os.homedir(),
    out: (line) => console.log(line),
  };
}

export function unitDir(env: NodeJS.ProcessEnv, home: string): string {
  return path.join(env.XDG_CONFIG_HOME || path.join(home, ".config"), "systemd", "user");
}

/** The panel's control-plane binary: XMESH_BIN, else `xmesh` on PATH (an absolute path that exists). */
export function findXmesh(env: NodeJS.ProcessEnv, exists: (p: string) => boolean = (p) => { try { return fs.statSync(p).isFile(); } catch { return false; } }): string | undefined {
  if (env.XMESH_BIN && path.isAbsolute(env.XMESH_BIN) && exists(env.XMESH_BIN)) return env.XMESH_BIN;
  for (const d of (env.PATH ?? "").split(":")) {
    if (d !== "" && path.isAbsolute(d) && exists(path.join(d, "xmesh"))) return path.join(d, "xmesh");
  }
  return undefined;
}

export interface InstallOptions extends UnitOptions {
  /** Plain-text management key to store (instead of keeping or generating one). */
  key?: string;
  rotateKey?: boolean;
  /** Allow a non-local listen address without TLS. */
  insecure?: boolean;
  /** Write the unit and enable it, but do not (re)start it. */
  noStart?: boolean;
  /** `loginctl enable-linger`, so the service runs without a login session. */
  linger?: boolean;
  /** Print the unit and change nothing. */
  print?: boolean;
  unitDir?: string;
}

function isLoopbackListen(listen: string): boolean {
  const host = listen.replace(/:\d+$/, "").replace(/^\[|\]$/g, "");
  return host === "localhost" || host === "::1" || /^127\./.test(host);
}

function systemctl(d: Deps, args: string[], what: string): Run {
  const r = d.run("systemctl", ["--user", ...args]);
  if (r.status !== 0) {
    const detail = (r.stderr || r.stdout).trim();
    const bus = /bus|XDG_RUNTIME_DIR|not been booted|offline/i.test(detail);
    throw new Error(`${what} failed${detail ? `: ${detail}` : ""}${bus ? "\n没有可用的 systemd 用户会话。请在登录会话中运行（SSH 登录即可），或先执行 `loginctl enable-linger` 并确认 XDG_RUNTIME_DIR 已设置。" : ""}`);
  }
  return r;
}

export interface InstallResult {
  unit: string;
  /** Set when this call created or replaced the key; the caller shows it once. */
  key?: string;
  keyFile: string;
  url: string;
}

export function installService(o: InstallOptions, d: Deps = realDeps()): InstallResult {
  if ((o.tlsCert === undefined) !== (o.tlsKey === undefined)) throw new Error("--tls-cert and --tls-key go together");
  if (!o.insecure && o.tlsCert === undefined && !isLoopbackListen(o.listen)) {
    throw new Error(`监听 ${o.listen} 不是本机地址且没有 TLS：管理密钥和会话会以明文传输。请加 --tls-cert/--tls-key，或只监听 127.0.0.1 并用 SSH 隧道；确需明文时加 --insecure。`);
  }
  for (const f of [o.tlsCert, o.tlsKey]) {
    if (f !== undefined) fs.accessSync(f, fs.constants.R_OK);
  }
  if (o.key !== undefined && !validKey(o.key)) throw new Error(`管理密钥须为 ${MIN_KEY_LENGTH}–512 个字符，且不含空白`);
  const unit = renderUnit(o);
  const secure = o.tlsCert !== undefined;
  const shown = o.listen.startsWith(":") ? `127.0.0.1${o.listen}` : o.listen;
  const result = { keyFile: o.keyFile, url: `${secure ? "https" : "http"}://${shown}/` };
  if (o.print) {
    d.out(unit);
    return { unit: "", ...result };
  }

  // The key first: a service that starts without one would generate its own and only log it.
  let shownKey: string | undefined;
  if (o.key !== undefined) {
    let current: string | undefined;
    try { current = fs.readFileSync(o.keyFile, "utf8").trim(); } catch { current = undefined; }
    if (current !== o.key) { writeKeyFile(o.keyFile, o.key); shownKey = o.key; }
  } else {
    const loaded = loadKey(o.keyFile, o.rotateKey === true);
    if (loaded.created) shownKey = loaded.key;
  }
  fs.mkdirSync(o.dataDir, { recursive: true, mode: 0o700 });

  const dir = o.unitDir ?? unitDir(d.env, d.home);
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, `${SERVICE_NAME}.service`);
  fs.writeFileSync(file, unit, { mode: 0o644 });
  d.out(`已写入 ${file}`);

  systemctl(d, ["daemon-reload"], "systemctl daemon-reload");
  systemctl(d, ["enable", SERVICE_NAME], "systemctl enable");
  // restart, not start: a running service must pick up a changed unit or key.
  if (!o.noStart) systemctl(d, ["restart", SERVICE_NAME], "systemctl restart");
  if (o.linger) {
    const r = d.run("loginctl", ["enable-linger"]);
    if (r.status !== 0) d.out(`警告：loginctl enable-linger 失败（${(r.stderr || r.stdout).trim() || "无输出"}）。没有登录会话时服务不会运行；可让管理员执行 loginctl enable-linger ${os.userInfo().username}。`);
  }
  d.out(o.noStart ? `服务 ${SERVICE_NAME} 已启用，尚未启动。` : `服务 ${SERVICE_NAME} 已启动：${result.url}`);
  return { unit: file, ...(shownKey !== undefined && { key: shownKey }), ...result };
}

export interface UninstallOptions {
  /** Also delete the key file and the data directory. */
  purge?: boolean;
  dataDir: string;
  keyFile: string;
  unitDir?: string;
}

export function uninstallService(o: UninstallOptions, d: Deps = realDeps()): void {
  const file = path.join(o.unitDir ?? unitDir(d.env, d.home), `${SERVICE_NAME}.service`);
  // Stopping and disabling are best effort: the unit may never have been enabled or started.
  d.run("systemctl", ["--user", "disable", "--now", SERVICE_NAME]);
  fs.rmSync(file, { force: true });
  systemctl(d, ["daemon-reload"], "systemctl daemon-reload");
  d.out(`已移除服务 ${SERVICE_NAME}`);
  if (o.purge) {
    fs.rmSync(o.keyFile, { force: true });
    fs.rmSync(o.dataDir, { recursive: true, force: true });
    d.out(`已删除 ${o.keyFile} 和 ${o.dataDir}`);
  } else {
    d.out(`保留了管理密钥 ${o.keyFile} 和数据目录 ${o.dataDir}（加 --purge 删除）`);
  }
}
