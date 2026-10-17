import type { ExternalKey, CopyKind, PathKind } from "../shared/ipc";
import type { Translate, MessageKey } from "../shared/i18n";
import { hasCapability } from "../shared/control";
import { type Snapshot, deriveIcon } from "../shared/state";

export type Action =
  | { type: "open-panel" }
  | { type: "drain" }
  | { type: "resume" }
  | { type: "copy"; kind: CopyKind }
  | { type: "open-path"; kind: PathKind }
  | { type: "open-external"; key: ExternalKey }
  | { type: "set-login-item"; enabled: boolean }
  | { type: "select-instance"; id: string }
  | { type: "restart-kernel" }
  | { type: "start-kernel"; node: string }
  | { type: "restart-unresponsive"; node: string }
  | { type: "stop-kernel" }
  | { type: "quit" };

export interface MenuItemSpec {
  id: string;
  kind: "normal" | "separator" | "checkbox" | "radio" | "submenu";
  label: string;
  enabled: boolean;
  checked?: boolean;
  action?: Action;
  submenu?: MenuItemSpec[];
}

const sep = (id: string): MenuItemSpec => ({ id, kind: "separator", label: "", enabled: false });
const info = (id: string, label: string): MenuItemSpec => ({ id, kind: "normal", label, enabled: false });
const item = (id: string, label: string, action: Action, enabled = true): MenuItemSpec => ({
  id,
  kind: "normal",
  label,
  enabled,
  action,
});

export function statusLabelKey(snap: Snapshot): MessageKey {
  switch (deriveIcon(snap.connection)) {
    case "offline":
      return snap.unresponsiveNodes.length > 0 ? "status.unresponsive" : "status.offline";
    case "mismatch":
      return "status.mismatch";
    case "draining":
      return "status.draining";
    case "degraded":
      return "status.degraded";
    case "restart":
      return "status.restart";
    case "ok":
      return "status.ok";
  }
}

/** Builds the tray menu as plain data; `tray.ts` turns it into a native `Menu`. */
export function buildMenu(snap: Snapshot, t: Translate): MenuItemSpec[] {
  const out: MenuItemSpec[] = [];
  const c = snap.connection;

  out.push(info("header.state", t("menu.state", { status: t(statusLabelKey(snap)) })));

  if (c.kind === "connected") {
    const s = c.status;
    out.splice(0, 0, info("header.node", t("menu.node", { name: s.hostname })));
    out.push(info("header.network", t("menu.network", { mode: s.network.mode })));
    out.push(info("header.peers", t("menu.peers", { count: s.peers.online })));
    s.warnings.forEach((w, i) => out.push(info(`warning.${i}.${w.code}`, w.message)));
    out.push(sep("sep.activity"));
    out.push(info("activity.pty", t("menu.pty", { used: s.limits.pty_in_use, max: s.limits.pty_max_sessions })));
    out.push(info("activity.exec", t("menu.exec", { used: s.limits.exec_shared_in_use, max: s.limits.exec_shared })));
  } else if (c.kind === "mismatch") {
    out.push(info("hint.mismatch", t("menu.mismatchHint", { kernel: c.kernelVersion || "?", api: c.apiVersion || "?" })));
  } else if (snap.unresponsiveNodes.length > 0) {
    out.push(info("hint.unresponsive", t("menu.unresponsiveHint")));
  } else {
    out.push(info("hint.offline", t("menu.offlineHint")));
  }

  out.push(sep("sep.actions"));
  if (c.kind !== "mismatch") out.push(item("action.openPanel", t("menu.openPanel"), { type: "open-panel" }));
  if (c.kind === "offline") {
    for (const node of snap.unresponsiveNodes) {
      const label = snap.unresponsiveNodes.length === 1 ? t("menu.restartKernel") : t("menu.restartKernelNamed", { name: node });
      out.push(item(`action.restartUnresponsive.${node}`, label, { type: "restart-unresponsive", node }));
    }
    for (const node of snap.stoppedNodes) {
      const label = snap.stoppedNodes.length === 1 ? t("menu.startKernel") : t("menu.startKernelNamed", { name: node });
      out.push(item(`action.startKernel.${node}`, label, { type: "start-kernel", node }));
    }
  }

  if (c.kind === "connected") {
    const s = c.status;
    const canDrain = hasCapability(s.control.capabilities, "drain");
    out.push(
      s.drain.draining
        ? item("action.resume", t("menu.resume"), { type: "resume" }, canDrain)
        : item("action.drain", t("menu.drain"), { type: "drain" }, canDrain),
    );
    const gw = s.gateway !== undefined;
    out.push(
      item("action.copyGateway", t("menu.copyGateway"), { type: "copy", kind: "gatewayUrl" }, gw),
      item("action.copyClaude", t("menu.copyClaude"), { type: "copy", kind: "claudeCodeCommand" }, gw),
      item("action.copyCodex", t("menu.copyCodex"), { type: "copy", kind: "codexConfig" }, gw),
      item("action.openConfig", t("menu.openConfig"), { type: "open-path", kind: "config" }, s.paths.config_file !== ""),
      item("action.openLogs", t("menu.openLogs"), { type: "open-path", kind: "logs" }, s.paths.log_dir !== null),
      item("action.openAudit", t("menu.openAudit"), { type: "open-path", kind: "audit" }, s.paths.audit_dir !== null),
    );
  } else if (c.kind === "mismatch") {
    out.push(item("action.upgrade", t("menu.upgrade"), { type: "open-external", key: "docs" }));
  }

  if (snap.instances.length > 1) {
    out.push(sep("sep.instances"));
    out.push({
      id: "instances",
      kind: "submenu",
      label: t("menu.instances"),
      enabled: true,
      submenu: snap.instances.map((i) => ({
        id: `instance.${i.id}`,
        kind: "radio",
        label: i.label,
        enabled: true,
        checked: i.id === snap.selectedInstance,
        action: { type: "select-instance", id: i.id },
      })),
    });
  }

  out.push(sep("sep.settings"));
  out.push({
    id: "settings",
    kind: "submenu",
    label: t("menu.settings"),
    enabled: true,
    submenu: [
      {
        id: "settings.loginItem",
        kind: "checkbox",
        label: t("menu.loginItem"),
        enabled: true,
        checked: snap.settings.loginItem,
        action: { type: "set-login-item", enabled: !snap.settings.loginItem },
      },
      item("settings.restartKernel", t("menu.restartKernel"), { type: "restart-kernel" }, c.kind === "connected"),
    ],
  });

  out.push(sep("sep.quit"));
  out.push(item("action.quit", t("menu.quit"), { type: "quit" }));
  out.push(item("action.stopKernel", t("menu.stopKernel"), { type: "stop-kernel" }, c.kind === "connected"));
  return out;
}

/** Flattens ids for assertions. */
export function menuIds(items: MenuItemSpec[]): string[] {
  const ids: string[] = [];
  for (const i of items) {
    ids.push(i.id);
    if (i.submenu) ids.push(...menuIds(i.submenu));
  }
  return ids;
}

export function findItem(items: MenuItemSpec[], id: string): MenuItemSpec | undefined {
  for (const i of items) {
    if (i.id === id) return i;
    const nested = i.submenu ? findItem(i.submenu, id) : undefined;
    if (nested) return nested;
  }
  return undefined;
}
