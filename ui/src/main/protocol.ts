import { randomBytes } from "node:crypto";
import * as fs from "node:fs";
import { protocol } from "electron";
import { PANEL_SCHEME, mimeFor, panelHeaders, resolveUiFile } from "./policy";

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
    const file = resolveUiFile(root, request.url);
    const type = file === undefined ? undefined : mimeFor(file);
    if (file === undefined || type === undefined) return new Response(null, { status: 404 });
    try {
      let body = await fs.promises.readFile(file);
      const nonce = randomBytes(24).toString("base64");
      const headers = panelHeaders(type);
      if (type.startsWith("text/html")) {
        body = Buffer.from(body.toString("utf8").replaceAll("__XMESH_STYLE_NONCE__", nonce));
        headers["Content-Security-Policy"] = headers["Content-Security-Policy"]!.replaceAll("__XMESH_STYLE_NONCE__", nonce);
      }
      return new Response(request.method === "HEAD" ? null : body, { status: 200, headers });
    } catch {
      return new Response(null, { status: 404 });
    }
  });
}
