// Sandboxed preload: it can only `require("electron")`, so channel names are
// repeated here as literals. test/unit/preload.test.ts loads this file against
// a fake electron and checks the names against src/shared/ipc.ts.
//
// Only a fixed set of functions crosses into the page. Nothing here exposes
// ipcRenderer, a generic invoke/send/on, or any Node API.

import { contextBridge, ipcRenderer } from "electron";
import type { CallEventData } from "../shared/control";
import type { PanelApi } from "../shared/ipc";
import type { Snapshot } from "../shared/state";

const invoke = (channel: string, ...args: unknown[]): Promise<never> =>
  ipcRenderer.invoke(channel, ...args) as Promise<never>;

const api: PanelApi = {
  getSnapshot: () => invoke("xmesh:get-snapshot"),
  onSnapshot: (listener) => {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    const handler = (_event: unknown, snapshot: Snapshot): void => listener(snapshot);
    ipcRenderer.on("xmesh:snapshot", handler);
    return () => {
      ipcRenderer.removeListener("xmesh:snapshot", handler);
    };
  },
  drain: (mode) => (mode === undefined ? invoke("xmesh:drain") : invoke("xmesh:drain", mode)),
  resume: () => invoke("xmesh:resume"),
  selectInstance: (id) => invoke("xmesh:select-instance", id),
  copy: (kind) => invoke("xmesh:copy", kind),
  openPath: (kind) => invoke("xmesh:open-path", kind),
  setLoginItem: (enabled) => invoke("xmesh:set-login-item", enabled),
  openExternal: (key) => invoke("xmesh:open-external", key),
  getSessions: () => invoke("xmesh:get-sessions"),
  getConfig: () => invoke("xmesh:get-config"),
  putConfig: (request) => invoke("xmesh:put-config", request),
  listPats: () => invoke("xmesh:list-pats"),
  createPat: (args) => invoke("xmesh:create-pat", args),
  rotatePat: (name) => invoke("xmesh:rotate-pat", name),
  deletePat: (name) => invoke("xmesh:delete-pat", name),
  listIngressClients: () => invoke("xmesh:list-ingress-clients"),
  revokeIngressClient: (clientId) => invoke("xmesh:revoke-ingress-client", clientId),
  rotateIngressPassword: (password) => invoke("xmesh:rotate-ingress-password", password),
  exportPair: () => invoke("xmesh:export-pair"),
  importPair: (args) => invoke("xmesh:import-pair", args),
  queryAudit: (query) => invoke("xmesh:query-audit", query),
  getRecentCalls: () => invoke("xmesh:get-recent-calls"),
  onCalls: (listener) => {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    const handler = (_event: unknown, calls: CallEventData[]): void => listener(calls);
    ipcRenderer.on("xmesh:calls", handler);
    return () => {
      ipcRenderer.removeListener("xmesh:calls", handler);
    };
  },
  runSetup: (params) => invoke("xmesh:run-setup", params),
  pickPath: (kind) => invoke("xmesh:pick-path", kind),
};

contextBridge.exposeInMainWorld("xmesh", Object.freeze(api));
