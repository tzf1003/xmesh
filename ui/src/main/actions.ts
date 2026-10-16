import * as fs from "node:fs";
import * as path from "node:path";
import { EXTERNAL_LINKS, type ActionResult, type CopyKind, type ExternalKey, type PathKind } from "../shared/ipc";
import type { Translate } from "../shared/i18n";
import { ControlRequestError } from "../shared/control";
import { CliError, type CliCommand } from "./cli";
import { clipboardText } from "./connect";
import type { ShellController } from "./controller";
import type { Action } from "./menu";

/** Everything with a side effect on the desktop, so the logic above it can be tested without Electron. */
export interface Platform {
  writeClipboard(text: string): void;
  openPath(p: string): Promise<string>;
  openExternal(url: string): Promise<void>;
  confirm(opts: { title: string; detail: string; confirmLabel: string; cancelLabel: string }): Promise<boolean>;
  showPanel(): void;
  quit(): void;
  runCli(command: CliCommand): Promise<unknown>;
  notifyError(message: string): void;
}

const CONFIG_EXTENSIONS = new Set([".yaml", ".yml", ".json", ".toml", ".txt"]);

const ok: ActionResult = { ok: true };
const fail = (code: string, message: string): ActionResult => ({ ok: false, error: { code, message } });

/**
 * The operations behind the tray menu and the panel IPC. Both go through the
 * same methods, so a panel cannot do anything the menu cannot.
 */
export class Actions {
  constructor(
    private readonly controller: ShellController,
    private readonly platform: Platform,
    private readonly t: Translate,
  ) {}

  dispatch(a: Action): Promise<ActionResult> {
    switch (a.type) {
      case "open-panel":
        return this.openPanel();
      case "drain":
        return this.drain();
      case "resume":
        return this.resume();
      case "copy":
        return this.copy(a.kind);
      case "open-path":
        return this.openPath(a.kind);
      case "open-external":
        return this.openExternal(a.key);
      case "set-login-item":
        return this.setLoginItem(a.enabled);
      case "select-instance":
        return this.selectInstance(a.id);
      case "restart-kernel":
        return this.runCli("restart");
      case "stop-kernel":
        return this.stopKernel();
      case "quit":
        this.platform.quit();
        return Promise.resolve(ok);
    }
  }

  async openPanel(): Promise<ActionResult> {
    this.platform.showPanel();
    return ok;
  }

  drain(): Promise<ActionResult> {
    return this.control(() => this.controller.drain());
  }

  resume(): Promise<ActionResult> {
    return this.control(() => this.controller.resume());
  }

  async selectInstance(id: string): Promise<ActionResult> {
    if (!this.controller.snapshot.instances.some((i) => i.id === id)) return fail("not_found", "unknown instance");
    this.controller.selectInstance(id);
    return ok;
  }

  async setLoginItem(enabled: boolean): Promise<ActionResult> {
    this.controller.setLoginItem(enabled);
    return ok;
  }

  async copy(kind: CopyKind): Promise<ActionResult> {
    const c = this.controller.snapshot.connection;
    const text = clipboardText(kind, c.kind === "connected" ? c.status.gateway?.url : undefined);
    if (text === undefined) return fail("unavailable", "no gateway address");
    this.platform.writeClipboard(text);
    return ok;
  }

  async openPath(kind: PathKind): Promise<ActionResult> {
    const c = this.controller.snapshot.connection;
    const paths = c.kind === "connected" ? c.status.paths : undefined;
    const p = paths === undefined ? undefined : { config: paths.config_file, logs: paths.log_dir, audit: paths.audit_dir }[kind];
    if (p === undefined || p === null || p === "") return fail("unavailable", "path unknown");
    const problem = checkOpenablePath(kind, p);
    if (problem) return fail("invalid_path", problem);
    const err = await this.platform.openPath(p);
    return err === "" ? ok : fail("open_failed", err);
  }

  async openExternal(key: ExternalKey): Promise<ActionResult> {
    await this.platform.openExternal(EXTERNAL_LINKS[key]);
    return ok;
  }

  async stopKernel(): Promise<ActionResult> {
    const yes = await this.platform.confirm({
      title: this.t("dialog.stopKernel.title"),
      detail: this.t("dialog.stopKernel.detail"),
      confirmLabel: this.t("dialog.stopKernel.confirm"),
      cancelLabel: this.t("dialog.cancel"),
    });
    return yes ? this.runCli("stop") : ok;
  }

  private async runCli(command: CliCommand): Promise<ActionResult> {
    try {
      await this.platform.runCli(command);
      return ok;
    } catch (e) {
      this.platform.notifyError(this.t("dialog.cliFailed", { command: `xmesh ${command}` }));
      return fail(e instanceof CliError ? "cli_failed" : "internal", e instanceof Error ? e.message : String(e));
    }
  }

  private async control(fn: () => Promise<void>): Promise<ActionResult> {
    try {
      await fn();
      return ok;
    } catch (e) {
      if (e instanceof ControlRequestError) return fail(e.code, e.message);
      return fail("unavailable", e instanceof Error ? e.message : String(e));
    }
  }
}

/** Returns a reason string when `p` must not be handed to the OS "open" call. */
export function checkOpenablePath(kind: PathKind, p: string, stat: typeof fs.statSync = fs.statSync): string {
  if (!path.isAbsolute(p)) return "path is not absolute";
  let st: fs.Stats;
  try {
    st = stat(p);
  } catch {
    return "path does not exist";
  }
  if (kind === "config") {
    if (!st.isFile()) return "config path is not a file";
    if (!CONFIG_EXTENSIONS.has(path.extname(p).toLowerCase())) return "config file type is not allowed";
    return "";
  }
  return st.isDirectory() ? "" : "path is not a directory";
}
