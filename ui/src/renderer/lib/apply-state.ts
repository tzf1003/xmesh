// What the panel says about the configuration after a save, and for as long as the kernel disagrees with the file.
// The kernel is the source of truth: its status says whether a restart is pending and which valid settings it could
// not put into effect. A page that has just saved adds the answer to its own save, so the bar does not wait for the
// next status refresh. No page decides what the bar shows.

import type { StatusResponse } from "../../shared/control";
import type { SaveOutcome } from "./reload";

export interface ApplyView {
  /** Some saved settings only take effect after the kernel restarts. */
  restart: boolean;
  /** The settings that wait for the restart, as readable names. Empty when only the status knows about the restart. */
  waiting: string[];
  /** Valid settings the running kernel did not take, with the kernel's reason. */
  problems: string[];
  /** The file on disk cannot be loaded; the kernel keeps running on the last good one. */
  loadError?: string;
  /** A save just went through and nothing is left over: everything is in effect. */
  applied: boolean;
}

/** The settings that are cold by design, by config pointer prefix, as the names the panel uses. */
const SETTING_NAMES: [prefix: string, name: string][] = [
  ["/node/name", "节点名称"],
  ["/node/roles", "Agent 角色"],
  ["/mesh", "组网方式"],
  ["/audit", "审计日志"],
  ["/agent/policy/windows_appcontainer", "Windows 沙箱"],
];

/** A config pointer as a name for the user; a pointer the panel has no name for is shown as it is. */
export function settingName(pointer: string): string {
  const hit = SETTING_NAMES.find(([prefix]) => pointer === prefix || pointer.startsWith(`${prefix}/`));
  return hit ? hit[1] : pointer;
}

/** The settings still waiting for a restart, without repeats. */
export function waitingSettings(paths: string[]): string[] {
  return [...new Set(paths.map(settingName))];
}

/**
 * `fresh` is the answer to the save the user just made, `status` what the kernel reports now (absent while it is not
 * connected). A save settles the apply errors itself, so while it is shown the status's list is not mixed in; the
 * restart is the kernel's own too, so either source can ask for it.
 */
export function applyView(fresh: SaveOutcome | undefined, status: StatusResponse | undefined, baseline: StatusResponse | undefined = status): ApplyView {
  const failed = fresh !== undefined && (fresh.loadError !== undefined || fresh.applyErrors.length > 0);
  const sameBoot = !status || !baseline || (status.control.boot_id === baseline.control.boot_id && status.network.node_id === baseline.network.node_id);
  const current = fresh && (status === baseline || (failed && sameBoot)) ? fresh : undefined;
  const restart = (current?.restart ?? false) || (status?.restart_required ?? false);
  const waiting = waitingSettings(current?.paths ?? []);
  const problems = current ? [...current.applyErrors] : (status?.warnings ?? []).filter((w) => w.code === "policy_reload_failed").map((w) => w.message);
  const loadError = current?.loadError;
  return { restart, waiting, problems, loadError, applied: fresh !== undefined && !restart && problems.length === 0 && loadError === undefined };
}

/** Whether there is anything to say: a save to confirm, a restart to ask for, or something that did not apply. */
export function hasNews(v: ApplyView): boolean {
  return v.applied || v.restart || v.problems.length > 0 || v.loadError !== undefined;
}
