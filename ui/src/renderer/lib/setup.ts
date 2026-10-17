import type { SetupParams } from "../../shared/ipc";
import { DEFAULT_CONTROL_URL, parseSetup, type SetupProblem } from "../../shared/setup";

export interface WizardForm {
  nodeName: string;
  mode: "lan" | "tailnet";
  agent: boolean;
  gateway: boolean;
  workspace: string;
  controlUrl: string;
  authkeyFile: string;
}

export const INITIAL_FORM: WizardForm = {
  nodeName: "",
  mode: "lan",
  agent: true,
  gateway: true,
  workspace: "",
  controlUrl: DEFAULT_CONTROL_URL,
  authkeyFile: "",
};

export function toParams(f: WizardForm): unknown {
  const roles: string[] = [];
  if (f.agent) roles.push("agent");
  if (f.gateway) roles.push("gateway");
  const p: Record<string, unknown> = { nodeName: f.nodeName.trim(), mode: f.mode, roles, workspace: f.workspace.trim() };
  if (f.mode === "tailnet") {
    p.controlUrl = f.controlUrl.trim();
    p.authkeyFile = f.authkeyFile.trim();
  }
  return p;
}

export type FormCheck = { ok: true; params: SetupParams } | { ok: false; problem: SetupProblem };

/**
 * Same validation the main process applies, so the form never offers a step the main process would refuse.
 * The review text comes from the main process, which knows where the files go, so it matches what gets written.
 */
export function checkForm(f: WizardForm): FormCheck {
  const r = parseSetup(toParams(f));
  return r.ok ? { ok: true, params: r.value } : { ok: false, problem: r.problem };
}
