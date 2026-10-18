import { ciCounts } from "./ci";
import { type Peer, type StatusResponse, isDegrading } from "./control";
import { DEFAULT_DISPLAY, type DisplayPrefs } from "./display";
import type { Locale } from "./i18n";
import { EMPTY_UPDATE, type UpdateState } from "./update";

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
  /** Text size and typefaces of the panel. */
  display: DisplayPrefs;
}

/** Everything the tray menu and the panel render from. */
export interface Snapshot {
  connection: Connection;
  instances: InstanceInfo[];
  /** Nodes with a config file but no running kernel; the offline view offers to start them. */
  stoppedNodes: string[];
  /** Nodes whose control manifest is there but whose kernel does not answer; the offline view offers to restart them. */
  unresponsiveNodes: string[];
  /** Nodes whose control endpoint failed and whose recorded process has exited. */
  staleNodes: string[];
  /** Stale system service nodes whose state needs administrator cleanup. */
  protectedStaleNodes: string[];
  selectedInstance: string;
  settings: UiSettings;
  /** The CLI as the user types it in a terminal, naming the binary the app runs (see cliCommandText). */
  cliCommand: string;
  /** Version check and one-click update of the app and the kernel inside it. */
  update: UpdateState;
}

/**
 * CTR-CTL-03 / RUL-18, first match wins: not running, version mismatch,
 * draining, restart required, degraded, healthy. Degraded looks only at
 * warnings of severity `warning` or `error`, and at CI runners that need the
 * user; `info` warnings (restart_required, allow_dangerous) do not turn the
 * icon yellow.
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
      // A runner that waits for the user (a stolen session, a runner GitHub removed) is as degraded as an offline peer.
      if (s.warnings.some(isDegrading) || ciCounts(s.ci?.bindings).broken > 0) return "degraded";
      return "ok";
    }
  }
}

export const EMPTY_SNAPSHOT: Snapshot = {
  connection: { kind: "offline" },
  instances: [],
  stoppedNodes: [],
  unresponsiveNodes: [],
  staleNodes: [],
  protectedStaleNodes: [],
  selectedInstance: "",
  settings: { loginItem: false, locale: "zh", display: DEFAULT_DISPLAY },
  cliCommand: "xmesh",
  update: EMPTY_UPDATE,
};
