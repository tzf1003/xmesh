// Bundles the Linux web management panel into dist/web (run after build-renderer.mjs):
//   dist/web/xmesh-webui.js   the server, one CommonJS file for Node 22 with no node_modules
//   dist/web/public/          the login shim (web.js) and the same panel bundle the desktop app shows
//   dist/web/VERSION          $VERSION, or package.json's version
// `node dist/web/xmesh-webui.js --help` lists the options; see docs/design/12-ui-and-control.md.

import { build } from "esbuild";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const renderer = path.join(root, "dist", "renderer");
const out = path.join(root, "dist", "web");
const pub = path.join(out, "public");

if (!fs.existsSync(path.join(renderer, "app.js"))) throw new Error("dist/renderer is missing: run scripts/build-renderer.mjs first");
fs.rmSync(out, { recursive: true, force: true });
fs.mkdirSync(pub, { recursive: true });

const minify = process.env.XMESH_UI_MINIFY === "1";

await build({
  entryPoints: [path.join(root, "src", "renderer", "web", "boot.ts")],
  outfile: path.join(pub, "web.js"),
  bundle: true, format: "iife", platform: "browser", target: "chrome120",
  minify, sourcemap: false, legalComments: "none", logLevel: "warning",
});
for (const f of ["index.html", "web.css"]) fs.copyFileSync(path.join(root, "src", "renderer", "web", f), path.join(pub, f));
for (const f of ["app.js", "app.css", "styles.css", "xterm.css"]) fs.copyFileSync(path.join(renderer, f), path.join(pub, f));
fs.copyFileSync(path.join(root, "build", "icon.png"), path.join(pub, "icon.png"));

const result = await build({
  entryPoints: [path.join(root, "src", "web", "main.ts")],
  outfile: path.join(out, "xmesh-webui.js"),
  bundle: true, platform: "node", format: "cjs", target: "node22",
  minify, legalComments: "eof", logLevel: "warning", metafile: true,
  banner: { js: "#!/usr/bin/env node" },
  // Nothing on the server side may reach Electron; the build fails instead of shipping a broken require.
  external: ["electron"],
});
const electron = Object.values(result.metafile.outputs).some((o) => o.imports.some((i) => i.path === "electron"));
if (electron) throw new Error("src/web pulls in the electron module; keep Electron imports out of the shared main-process modules");
fs.chmodSync(path.join(out, "xmesh-webui.js"), 0o755);

const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
fs.writeFileSync(path.join(out, "VERSION"), `${(process.env.VERSION || pkg.version).replace(/^v/, "")}\n`);
