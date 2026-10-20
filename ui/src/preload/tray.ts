import { contextBridge, ipcRenderer } from "electron";
import type { TrayApi, TrayState } from "../shared/tray";
const api: TrayApi = {
  getState: () => ipcRenderer.invoke("xmesh:tray-state"),
  command: (command) => ipcRenderer.invoke("xmesh:tray-command", command),
  hide: () => ipcRenderer.invoke("xmesh:tray-hide"),
  onState: (listener) => {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    const handler = (_event: unknown, state: TrayState): void => listener(state);
    ipcRenderer.on("xmesh:tray-update", handler);
    return () => {
      ipcRenderer.removeListener("xmesh:tray-update", handler);
    };
  },
  onPresent: (listener) => {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    const handler = (_event: unknown, more: boolean): void => listener(more);
    ipcRenderer.on("xmesh:tray-present", handler);
    return () => {
      ipcRenderer.removeListener("xmesh:tray-present", handler);
    };
  },
};
contextBridge.exposeInMainWorld("xmeshTray", Object.freeze(api));
