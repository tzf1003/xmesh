import { randomUUID } from "node:crypto";
import { EventEmitter } from "node:events";
import { ciCounts } from "../shared/ci";
import { deriveIcon } from "../shared/state";
import type { Translate } from "../shared/i18n";
import type { PanelTarget, TrayCommand, TrayConfirmation, TrayReply, TrayState } from "../shared/tray";
import type { Actions } from "./actions";
import type { ShellController } from "./controller";
import { buildMenu, findItem, statusLabelKey, type MenuItemSpec } from "./menu";

const fail = (code: string, message: string): TrayReply => ({
  ok: false,
  error: { code, message },
});
export class TrayService extends EventEmitter<{ change: [] }> {
  private revision = 0;
  private busy = false;
  private refreshing = false;
  private confirmation?: {
    value: TrayConfirmation;
    bootId: string | null;
    nodeBootId?: string;
    id: string;
    expires: number;
  };
  private readonly changed = (): void => {
    this.revision++;
    this.emit("change");
  };
  constructor(
    private readonly controller: ShellController,
    private readonly actions: Actions,
    private readonly t: Translate,
    private readonly host: {
      openPanel(target: PanelTarget): void;
      panelDirty(): boolean;
      copy(text: string): void;
    },
  ) {
    super();
    controller.on("change", this.changed);
    controller.on("activity", this.changed);
    controller.on("refreshed", this.changed);
  }
  dispose(): void {
    this.controller.off("change", this.changed);
    this.controller.off("activity", this.changed);
    this.controller.off("refreshed", this.changed);
    this.clearConfirmation();
    this.removeAllListeners();
  }
  clearConfirmation(): void {
    this.confirmation = undefined;
  }
  get state(): TrayState {
    const snap = this.controller.snapshot,
      c = snap.connection;
    const flatten = (items: MenuItemSpec[]): MenuItemSpec[] =>
      items.flatMap((x) => (x.submenu ? flatten(x.submenu) : x.action ? [x] : []));
    const connection: TrayState["connection"] =
      c.kind !== "connected"
        ? c
        : {
            kind: "connected",
            hostname: c.status.hostname,
            network: c.status.network.mode,
            draining: c.status.drain.draining,
            restartRequired: c.status.restart_required,
            warnings: c.status.warnings,
            peers: c.peers
              .filter((p) => !p.self)
              .map((p) => ({
                name: p.name,
                self: false,
                online: p.online,
                os: p.os,
                addrs: p.addrs,
                connection: p.connection,
                derp_region: p.derp_region,
                last_seen: p.last_seen,
                display_name: p.display_name,
                trust_state: p.trust_state,
                active_path: p.active_path,
                connection_state: p.connection_state,
                device_id: p.device_id,
                node_id: p.node_id,
              })),
            peersOnline: c.status.peers.online,
            peersTotal: c.status.peers.total,
            pty: {
              used: c.status.limits.pty_in_use,
              max: c.status.limits.pty_max_sessions,
            },
            exec: {
              used: c.status.limits.exec_shared_in_use,
              max: c.status.limits.exec_shared,
            },
            backends: c.status.backends.map((b) => ({
              name: b.name,
              policy: b.policy,
              state: b.state,
            })),
            ...(c.status.gateway && { gateway: { url: c.status.gateway.url } }),
            ...(c.status.ci && { ci: ciCounts(c.status.ci.bindings) }),
            kernelVersion: c.status.control.kernel_version,
          };
    return {
      revision: this.revision,
      instance: snap.selectedInstance,
      bootId: c.kind === "connected" ? c.status.control.boot_id : null,
      icon: deriveIcon(c),
      status: this.t(statusLabelKey(snap)),
      updatedAt: this.controller.lastUpdatedAt,
      refreshing: this.refreshing,
      busy: this.busy,
      connection,
      instances: snap.instances,
      stoppedNodes: snap.stoppedNodes,
      unresponsiveNodes: snap.unresponsiveNodes,
      staleNodes: snap.staleNodes,
      protectedStaleNodes: snap.protectedStaleNodes,
      locale: snap.settings.locale,
      display: snap.settings.display,
      loginItem: snap.settings.loginItem,
      operations: flatten(buildMenu(snap, this.t)).map(({ id, label, enabled, checked }) => ({
        id,
        label,
        enabled: enabled && !this.busy,
        ...(checked !== undefined && { checked }),
      })),
      activity: this.controller.recentActivity.slice().reverse(),
      updateVersion: snap.update.phase === "available" ? (snap.update.latest?.version ?? null) : null,
    };
  }
  async execute(command: TrayCommand): Promise<TrayReply> {
    const state = this.state;
    if (command.instance !== state.instance || command.bootId !== state.bootId)
      return fail(
        "conflict",
        state.locale === "zh" ? "节点状态已变化，请刷新后重试。" : "The node changed. Refresh and try again.",
      );
    if (this.busy)
      return fail("conflict", state.locale === "zh" ? "请等待当前操作完成。" : "Wait for the current operation.");
    const snap = this.controller.snapshot;
    if (command.op === "open-panel") {
      const c = snap.connection;
      if (
        (command.peer || command.device) &&
        (c.kind !== "connected" || !c.peers.some((p) => p.name === (command.peer ?? command.device)))
      )
        return fail("not_found", "Unknown device");
      if (command.device && (c.kind !== "connected" || !c.peers.some((p) => p.name === command.device && p.online)))
        return fail("unavailable", "Device is offline");
      this.host.openPanel({
        page: command.page,
        instance: state.instance,
        ...(command.peer && { peer: command.peer }),
        ...(command.device && { device: command.device }),
      });
      return { ok: true, data: {} };
    }
    if (command.op === "copy-peer") {
      const c = snap.connection;
      const peer = c.kind === "connected" ? c.peers.find((p) => p.name === command.peer) : undefined;
      if (!peer?.addrs.length) return fail("not_found", "Device address unavailable");
      this.host.copy(peer.addrs.join("\n"));
      return { ok: true, data: {} };
    }
    if (command.op === "confirm") {
      const pending = this.confirmation;
      this.clearConfirmation();
      if (
        !pending ||
        pending.value.token !== command.token ||
        pending.expires < Date.now() ||
        pending.value.instance !== state.instance ||
        pending.bootId !== state.bootId
      )
        return fail("conflict", "Confirmation expired; try again.");
      const item = findItem(buildMenu(snap, this.t), pending.id);
      if (!item?.enabled) return fail("unavailable", "Action is no longer available.");
      if (pending.value.action === "restart-unresponsive" && pending.value.node) {
        return this.perform(() => this.actions.confirmedUnresponsiveRestart(pending.value.node!, state.instance, pending.nodeBootId));
      }
      if (pending.value.action === "restart-unresponsive") return fail("invalid_args", "Missing node");
      const action = pending.value.action;
      return this.perform(() => this.actions.confirmedLifecycle(action, state.instance, state.bootId));
    }
    if (command.op === "refresh") {
      if (this.refreshing) return { ok: true, data: {} };
      this.refreshing = true;
      this.changed();
      try {
        await this.controller.refreshNow();
        return { ok: true, data: {} };
      } catch (e) {
        return fail("unavailable", e instanceof Error ? e.message : String(e));
      } finally {
        this.refreshing = false;
        this.changed();
      }
    }
    const item = findItem(buildMenu(snap, this.t), command.id);
    if (!item?.action || !item.enabled) return fail("unavailable", "Action is no longer available.");
    const action = item.action;
    if (action.type === "select-instance" && this.host.panelDirty()) {
      this.host.openPanel({ page: "config", instance: state.instance });
      return fail(
        "conflict",
        state.locale === "zh"
          ? "请先处理主窗口中未保存的修改。"
          : "Resolve the unsaved changes in the main window first.",
      );
    }
    if (action.type === "restart-kernel" || action.type === "stop-kernel") {
      if (state.connection.kind !== "connected") return fail("unavailable", "Kernel is not connected.");
      const value: TrayConfirmation = {
        token: randomUUID(),
        action: action.type,
        instance: state.instance,
        hostname: state.connection.hostname,
        pty: state.connection.pty.used,
        exec: state.connection.exec.used,
        ci: state.connection.ci?.busy ?? 0,
      };
      this.confirmation = {
        value,
        id: command.id,
        bootId: state.bootId,
        expires: Date.now() + 60_000,
      };
      return { ok: true, data: { confirmation: value } };
    }
    if (action.type === "restart-unresponsive") {
      const value: TrayConfirmation = { token: randomUUID(), action: action.type, node: action.node, instance: state.instance,
        hostname: action.node, pty: 0, exec: 0, ci: 0, impactKnown: false };
      this.confirmation = { value, id: command.id, bootId: state.bootId, nodeBootId: this.controller.nodeBootId(action.node), expires: Date.now() + 60_000 };
      return { ok: true, data: { confirmation: value } };
    }
    if (action.type === "open-panel") {
      this.host.openPanel({
        page: command.id === "action.update" ? "about" : "overview",
        instance: state.instance,
      });
      return { ok: true, data: {} };
    }
    return this.perform(() => this.actions.dispatch(action));
  }
  private async perform(
    run: () => Promise<{ ok: true } | { ok: false; error: { code: string; message: string } }>,
  ): Promise<TrayReply> {
    this.busy = true;
    this.changed();
    try {
      const r = await run();
      return r.ok ? { ok: true, data: {} } : r;
    } catch (e) {
      return fail("internal", e instanceof Error ? e.message : String(e));
    } finally {
      this.busy = false;
      this.changed();
    }
  }
}
