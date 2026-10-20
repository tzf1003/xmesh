import * as fs from "node:fs";
import * as path from "node:path";
import { EXTERNAL_LINKS, type ActionResult, type DataResult, type CopyKind, type ExternalKey, type GatewayCopyKind, type PathKind, type PickKind, isGatewayCopyKind } from "../shared/ipc";
import { ciCounts } from "../shared/ci";
import type { DisplayPrefs } from "../shared/display";
import type { Translate } from "../shared/i18n";
import { ControlRequestError, type DrainMode, type StatusResponse } from "../shared/control";
import { CliError, type CliCommand } from "./cli";
import { clipboardText, isSafeGatewayUrl } from "./connect";
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
  runCli(command: CliCommand, node?: string): Promise<unknown>;
  manageKernel?(command: CliCommand, node: string): Promise<unknown>;
  notifyError(message: string): void;
  /** Native chooser; resolves to the chosen absolute path or undefined on cancel. */
  pickPath(kind: PickKind): Promise<string | undefined>;
  /** Deletes the leftover control.json of a node whose kernel is gone. */
  removeStaleState(node: string): Promise<void>;
}

/** Supplies the gateway token that the copied connect commands carry. Throws when there is none. */
export interface GatewayCredentialSource {
  read(): Promise<string>;
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
    private readonly credentials: GatewayCredentialSource,
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
        // The tray has no panel to show an error in, so a failed copy becomes a notification.
        return this.copy(a.kind).then((r) => {
          if (!r.ok) this.platform.notifyError(this.t("menu.copyFailed"));
          return r;
        });
      case "open-path":
        return this.openPath(a.kind);
      case "open-external":
        return this.openExternal(a.key);
      case "set-login-item":
        return this.setLoginItem(a.enabled);
      case "select-instance":
        return this.selectInstance(a.id);
      case "restart-kernel":
        return this.runCli("restart", this.selectedNode());
      case "start-kernel":
        return this.startKernel(a.node);
      case "restart-unresponsive":
        return this.restartUnresponsive(a.node);
      case "cleanup-stale":
        return this.cleanupStale(a.node);
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

  drain(mode: DrainMode = "keep"): Promise<ActionResult> {
    return this.control(() => this.controller.drain(mode));
  }

  resume(): Promise<ActionResult> {
    return this.control(() => this.controller.resume());
  }

  async selectInstance(id: string): Promise<ActionResult> {
    if (!this.controller.snapshot.instances.some((i) => i.id === id)) return fail("not_found", "unknown instance");
    this.controller.selectInstance(id);
    return ok;
  }

  async setDisplay(prefs: DisplayPrefs): Promise<ActionResult> {
    this.controller.setDisplay(prefs);
    return ok;
  }

  async setLoginItem(enabled: boolean): Promise<ActionResult> {
    this.controller.setLoginItem(enabled);
    return ok;
  }

  /** Copies a terminal command (mesh login) or, for a gateway kind, the local gateway's own address. */
  async copy(kind: CopyKind): Promise<ActionResult> {
    if (isGatewayCopyKind(kind)) {
      const gateway = this.gatewayStatus();
      if (gateway === undefined) return fail("unavailable", "no gateway address");
      return this.copyGateway(kind, gateway.url);
    }
    const text = clipboardText(kind, undefined, undefined, this.controller.snapshot.cliCommand);
    if (text === undefined) return fail("invalid_args", "copy kind");
    this.platform.writeClipboard(text);
    return ok;
  }

  /**
   * Copies one of the gateway's addresses with the token in it. `url` must be an address the kernel
   * reports right now, so the page cannot make the clipboard carry anything else.
   */
  async copyGateway(kind: GatewayCopyKind, url: string): Promise<ActionResult> {
    const gateway = this.gatewayStatus();
    if (gateway === undefined) return fail("unavailable", "no gateway address");
    const known = gateway.url === url || gateway.addresses.some((a) => a.url === url);
    if (!known || !isSafeGatewayUrl(url)) return fail("invalid_args", "not a gateway address");
    let token: string;
    try {
      token = await this.credentials.read();
    } catch (e) {
      return fail("token_unavailable", e instanceof Error ? e.message : String(e));
    }
    const text = clipboardText(kind, url, token);
    if (text === undefined) return fail("token_unavailable", "token is not usable");
    this.platform.writeClipboard(text);
    return ok;
  }

  private gatewayStatus(): StatusResponse["gateway"] {
    const c = this.controller.snapshot.connection;
    return c.kind === "connected" ? c.status.gateway : undefined;
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

  async pickPath(kind: PickKind): Promise<DataResult<{ path: string | null }>> {
    const p = await this.platform.pickPath(kind);
    return { ok: true, data: { path: p ?? null } };
  }

  async openExternal(key: ExternalKey): Promise<ActionResult> {
    await this.platform.openExternal(EXTERNAL_LINKS[key]);
    return ok;
  }

  async stopKernel(): Promise<ActionResult> {
    const c = this.controller.snapshot.connection;
    const busy = c.kind === "connected" ? ciCounts(c.status.ci?.bindings).busy : 0;
    const yes = await this.platform.confirm({
      title: this.t("dialog.stopKernel.title"),
      detail: busy > 0 ? `${this.t("dialog.stopKernel.detail")}\n${this.t("dialog.stopKernel.ciBusy", { n: busy })}` : this.t("dialog.stopKernel.detail"),
      confirmLabel: this.t("dialog.stopKernel.confirm"),
      cancelLabel: this.t("dialog.cancel"),
    });
    return yes ? this.runCli("stop", this.selectedNode()) : ok;
  }

  /** Starts the service of a node whose config exists but whose kernel is not running. */
  async startKernel(node: string): Promise<ActionResult> {
    if (!this.controller.snapshot.stoppedNodes.includes(node)) return fail("not_found", "no stopped node with that name");
    return this.runCli("start", node);
  }

  /** Restarts the service of a node whose manifest is there but whose kernel does not answer. */
  async restartUnresponsive(node: string): Promise<ActionResult> {
    if (!this.controller.snapshot.unresponsiveNodes.includes(node)) return fail("not_found", "no unresponsive node with that name");
    return this.runCli("restart", node);
  }

  /** Clears a state file after rechecking that its recorded process has exited. */
  async cleanupStale(node: string): Promise<ActionResult> {
    if (!this.controller.snapshot.staleNodes.includes(node)) return fail("not_found", "no stale node with that name");
    if (this.controller.snapshot.protectedStaleNodes.includes(node)) return fail("forbidden", this.t("menu.staleAdminNamed", { name: node }));
    try {
      await this.platform.removeStaleState(node);
    } catch (e) {
      return fail("internal", e instanceof Error ? e.message : String(e));
    }
    this.controller.nudge();
    return ok;
  }

  /** The selected state-directory ID; runCli resolves the service node once. */
  private selectedNode(): string | undefined {
    return this.controller.snapshot.selectedInstance || undefined;
  }

  private async runCli(command: CliCommand, node?: string): Promise<ActionResult> {
    try {
      if (node && this.platform.manageKernel) await this.platform.manageKernel(command, node);
      else await this.platform.runCli(command, node ? this.controller.nodeName?.(node) || node : node);
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
