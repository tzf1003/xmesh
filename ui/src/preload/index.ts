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
  startKernel: (node) => invoke("xmesh:start-kernel", node),
  restartKernel: () => invoke("xmesh:restart-kernel"),
  readDenyFile: () => invoke("xmesh:read-deny-file"),
  writeDenyFile: (request) => invoke("xmesh:write-deny-file", request),
  createDenyFile: (request) => invoke("xmesh:create-deny-file", request),
  reloadConfig: () => invoke("xmesh:reload-config"),
  restartUnresponsive: (node) => invoke("xmesh:restart-unresponsive", node),
  selectInstance: (id) => invoke("xmesh:select-instance", id),
  copy: (kind) => invoke("xmesh:copy", kind),
  copyGateway: (request) => invoke("xmesh:copy-gateway", request),
  openPath: (kind) => invoke("xmesh:open-path", kind),
  setLoginItem: (enabled) => invoke("xmesh:set-login-item", enabled),
  setDisplay: (prefs) => invoke("xmesh:set-display", prefs),
  openExternal: (key) => invoke("xmesh:open-external", key),
  checkForUpdate: () => invoke("xmesh:check-update"),
  installUpdate: () => invoke("xmesh:install-update"),
  cancelUpdate: () => invoke("xmesh:cancel-update"),
  setUpdatePrefs: (prefs) => invoke("xmesh:set-update-prefs", prefs),
  getSessions: () => invoke("xmesh:get-sessions"),
  getConfig: () => invoke("xmesh:get-config"),
  putConfig: (request) => invoke("xmesh:put-config", request),
  patchConfig: (request) => invoke("xmesh:patch-config", request),
  listPats: () => invoke("xmesh:list-pats"),
  createPat: (args) => invoke("xmesh:create-pat", args),
  rotatePat: (name) => invoke("xmesh:rotate-pat", name),
  rotateGatewayPat: () => invoke("xmesh:rotate-gateway-pat"),
  deletePat: (name) => invoke("xmesh:delete-pat", name),
  updateGateway: (request) => invoke("xmesh:update-gateway", request),
  listIngressClients: () => invoke("xmesh:list-ingress-clients"),
  revokeIngressClient: (clientId) => invoke("xmesh:revoke-ingress-client", clientId),
  rotateIngressPassword: (password) => invoke("xmesh:rotate-ingress-password", password),
  exportPair: () => invoke("xmesh:export-pair"),
  importPair: (args) => invoke("xmesh:import-pair", args),
  getPairPassword: () => invoke("xmesh:get-pair-password"),
  setPairPassword: (password) => invoke("xmesh:set-pair-password", password),
  clearPairPassword: () => invoke("xmesh:clear-pair-password"),
  discoverPairs: () => invoke("xmesh:discover-pairs"),
  connectPair: (args) => invoke("xmesh:connect-pair", args),
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
  previewSetup: (params) => invoke("xmesh:preview-setup", params),
  runSetup: (params) => invoke("xmesh:run-setup", params),
  pickPath: (kind) => invoke("xmesh:pick-path", kind),
  mesh: (request) => invoke("xmesh:mesh", request),
};

contextBridge.exposeInMainWorld("xmesh", Object.freeze(api));
