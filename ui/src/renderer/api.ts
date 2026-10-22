import type { PanelApi } from "../shared/ipc";
import { WEB_SESSION_GLOBAL, type WebSessionApi } from "../shared/web";

/** The preload script's `window.xmesh`. It is absent only when the page is opened outside the app. */
export function getApi(): PanelApi {
  const api = (globalThis as { xmesh?: PanelApi }).xmesh;
  if (!api) throw new Error("window.xmesh is missing: the panel only runs inside the xmesh app");
  return api;
}

/** The web management panel's session (log out); undefined in the desktop app. */
export function getWebSession(): WebSessionApi | undefined {
  return (globalThis as unknown as Record<string, WebSessionApi | undefined>)[WEB_SESSION_GLOBAL];
}
