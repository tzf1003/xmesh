import type { PanelApi } from "../shared/ipc";

/** The preload script's `window.xmesh`. It is absent only when the page is opened outside the app. */
export function getApi(): PanelApi {
  const api = (globalThis as { xmesh?: PanelApi }).xmesh;
  if (!api) throw new Error("window.xmesh is missing: the panel only runs inside the xmesh app");
  return api;
}
