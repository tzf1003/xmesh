import * as fs from "node:fs";
import { protocol } from "electron";
import { PANEL_SCHEME, mimeFor, panelHeaders, resolvePanelFile } from "./policy";

/** Must run before `app.whenReady()`. */
export function registerPanelScheme(): void {
  protocol.registerSchemesAsPrivileged([
    { scheme: PANEL_SCHEME, privileges: { standard: true, secure: true, corsEnabled: true } },
  ]);
}

/** Serves the bundled panel from `root` over app://panel/, the only origin the window may show. */
export function servePanel(root: string): void {
  protocol.handle(PANEL_SCHEME, async (request) => {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response(null, { status: 405 });
    }
    const file = resolvePanelFile(root, request.url);
    const type = file === undefined ? undefined : mimeFor(file);
    if (file === undefined || type === undefined) return new Response(null, { status: 404 });
    try {
      const body = await fs.promises.readFile(file);
      return new Response(request.method === "HEAD" ? null : body, { status: 200, headers: panelHeaders(type) });
    } catch {
      return new Response(null, { status: 404 });
    }
  });
}
