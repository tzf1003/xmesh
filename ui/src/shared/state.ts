import { type Peer, type StatusResponse, isDegrading } from "./control";
import type { Locale } from "./i18n";

/** What the main process currently knows about the kernel. */
export type Connection =
  | { kind: "offline" }
  | { kind: "mismatch"; kernelVersion: string; apiVersion: string }
  | { kind: "connected"; status: StatusResponse; peers: Peer[] };

export type TrayIcon = "offline" | "ok" | "degraded" | "draining" | "restart" | "mismatch";

export interface InstanceInfo {
  /** Directory name under `<UserConfigDir>/xmesh`. */
  id: string;
  /** Node name when known (from a successful connection), else the id. */
  label: string;
}

export interface UiSettings {
  loginItem: boolean;
  /** Language of the tray and the panel; the panel has no other way to learn it. */
  locale: Locale;
}

/** Everything the tray menu and the panel render from. */
export interface Snapshot {
  connection: Connection;
  instances: InstanceInfo[];
  /** Nodes with a config file but no running kernel; the offline view offers to start them. */
  stoppedNodes: string[];
  selectedInstance: string;
  settings: UiSettings;
}

/**
 * CTR-CTL-03 / RUL-18, first match wins: not running, version mismatch,
 * draining, restart required, degraded, healthy. Degraded looks only at
 * warnings of severity `warning` or `error`; `info` ones (restart_required,
 * allow_dangerous) do not turn the icon yellow.
 */
export function deriveIcon(c: Connection): TrayIcon {
  switch (c.kind) {
    case "offline":
      return "offline";
    case "mismatch":
      return "mismatch";
    case "connected": {
      const s = c.status;
      if (s.drain.draining) return "draining";
      if (s.restart_required) return "restart";
      if (s.warnings.some(isDegrading)) return "degraded";
      return "ok";
    }
  }
}

export const EMPTY_SNAPSHOT: Snapshot = {
  connection: { kind: "offline" },
  instances: [],
  stoppedNodes: [],
  selectedInstance: "",
  settings: { loginItem: false, locale: "zh" },
};
