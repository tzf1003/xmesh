// Sandboxed preload: it can only `require("electron")`, so channel names are
// repeated here as literals. test/unit/preload.test.ts loads this file against
// a fake electron and checks the names against src/shared/ipc.ts.
//
// Only a fixed set of functions crosses into the page. Nothing here exposes
// ipcRenderer, a generic invoke/send/on, or any Node API.

import { contextBridge, ipcRenderer } from "electron";
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
  drain: () => invoke("xmesh:drain"),
  resume: () => invoke("xmesh:resume"),
  selectInstance: (id) => invoke("xmesh:select-instance", id),
  copy: (kind) => invoke("xmesh:copy", kind),
  openPath: (kind) => invoke("xmesh:open-path", kind),
  setLoginItem: (enabled) => invoke("xmesh:set-login-item", enabled),
  openExternal: (key) => invoke("xmesh:open-external", key),
};

contextBridge.exposeInMainWorld("xmesh", Object.freeze(api));
