import type { Backend, CallEventData, Peer, Warning } from "./control";
import type { DisplayPrefs } from "./display";
import type { ActionResult, DataResult } from "./ipc";
import type { InstanceInfo, TrayIcon } from "./state";

export const TRAY_ORIGIN = "app://tray";
export const TRAY_ENTRY = `${TRAY_ORIGIN}/index.html`;
export const TRAY_CHANNELS = {
  state: "xmesh:tray-state",
  command: "xmesh:tray-command",
  hide: "xmesh:tray-hide",
} as const;
export const TRAY_EVENT = "xmesh:tray-update";
export const TRAY_PRESENT = "xmesh:tray-present";
export const PANEL_NAVIGATION_EVENT = "xmesh:panel-navigation";
export const PANEL_NAVIGATION_CHANNEL = "xmesh:panel-ready";
export const PANEL_PAGES = [
  "overview",
  "devices",
  "runtime",
  "terminal",
  "sessions",
  "audit",
  "ci",
  "config",
  "settings",
  "about",
  "setup",
  "ingress",
] as const;
export type TrayPage = "overview" | "devices" | "services" | "activity";
export interface PanelTarget {
  page: (typeof PANEL_PAGES)[number];
  peer?: string;
  device?: string;
  instance: string;
}
export interface RecentCallView extends CallEventData {
  eventId: string;
  time: string;
  instanceId: string;
  bootId: string;
}
export type TrayPeer = Pick<
  Peer,
  | "name"
  | "self"
  | "online"
  | "os"
  | "addrs"
  | "connection"
  | "derp_region"
  | "last_seen"
  | "display_name"
  | "trust_state"
  | "active_path"
  | "connection_state"
  | "device_id"
  | "node_id"
>;
export interface TrayConnected {
  kind: "connected";
  hostname: string;
  network: string;
  draining: boolean;
  restartRequired: boolean;
  warnings: Warning[];
  peers: TrayPeer[];
  peersOnline: number;
  peersTotal: number;
  pty: { used: number; max: number };
  exec: { used: number; max: number };
  backends: Pick<Backend, "name" | "policy" | "state">[];
  gateway?: { url: string };
  ci?: { busy: number; idle: number; broken: number };
  kernelVersion: string;
}
export interface TrayState {
  revision: number;
  instance: string;
  bootId: string | null;
  icon: TrayIcon;
  status: string;
  updatedAt: number | null;
  refreshing: boolean;
  busy: boolean;
  connection: TrayConnected | { kind: "offline" } | { kind: "mismatch"; kernelVersion: string; apiVersion: string };
  instances: InstanceInfo[];
  stoppedNodes: string[];
  unresponsiveNodes: string[];
  staleNodes: string[];
  protectedStaleNodes: string[];
  locale: "zh" | "en";
  display: DisplayPrefs;
  loginItem: boolean;
  operations: {
    id: string;
    label: string;
    enabled: boolean;
    checked?: boolean;
  }[];
  activity: RecentCallView[];
  updateVersion: string | null;
}
export interface TrayConfirmation {
  token: string;
  action: "stop-kernel" | "restart-kernel" | "restart-unresponsive";
  node?: string;
  impactKnown?: boolean;
  instance: string;
  hostname: string;
  pty: number;
  exec: number;
  ci: number;
}
export type TrayCommand = { instance: string; bootId: string | null } & (
  | { op: "refresh" }
  | { op: "action"; id: string }
  | { op: "confirm"; token: string }
  | {
      op: "open-panel";
      page: PanelTarget["page"];
      peer?: string;
      device?: string;
    }
  | { op: "copy-peer"; peer: string }
);
export type TrayReply = DataResult<{ confirmation?: TrayConfirmation }>;
export interface TrayApi {
  getState(): Promise<TrayState>;
  onState(listener: (state: TrayState) => void): () => void;
  onPresent(listener: (more: boolean) => void): () => void;
  command(command: TrayCommand): Promise<TrayReply>;
  hide(): Promise<ActionResult>;
}
export function parseTrayCommand(value: unknown): TrayCommand | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return;
  const v = value as Record<string, unknown>;
  const text = (x: unknown, max: number): x is string =>
    typeof x === "string" && x.length <= max && !/[\u0000-\u001f]/.test(x);
  if (!text(v.instance, 128) || !(v.bootId === null || text(v.bootId, 128))) return;
  const common = ["instance", "bootId", "op"];
  let extra: string[];
  switch (v.op) {
    case "refresh":
      extra = [];
      break;
    case "action":
      if (!text(v.id, 256)) return;
      extra = ["id"];
      break;
    case "confirm":
      if (!text(v.token, 128)) return;
      extra = ["token"];
      break;
    case "copy-peer":
      if (!text(v.peer, 256)) return;
      extra = ["peer"];
      break;
    case "open-panel":
      if (!(PANEL_PAGES as readonly unknown[]).includes(v.page)) return;
      if (v.peer !== undefined && !text(v.peer, 256)) return;
      if (v.device !== undefined && !text(v.device, 256)) return;
      extra = ["page", "peer", "device"];
      break;
    default:
      return;
  }
  if (Object.keys(v).some((k) => !common.includes(k) && !extra.includes(k))) return;
  return v as unknown as TrayCommand;
}
