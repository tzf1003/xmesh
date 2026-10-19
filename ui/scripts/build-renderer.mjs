// Bundles the panel (src/renderer) into dist/renderer: app.js, styles.css, index.html.
// One IIFE, no code splitting, no eval, no external requests: the page is served
// from app://panel under the panel CSP; xterm styles are bundled beside it.

import { build } from "esbuild";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = path.join(root, "src", "renderer");
const out = path.join(root, "dist", "renderer");

fs.rmSync(out, { recursive: true, force: true });
fs.mkdirSync(out, { recursive: true });

await build({
  entryPoints: [path.join(src, "main.tsx")],
  outfile: path.join(out, "app.js"),
  bundle: true,
  format: "iife",
  platform: "browser",
  target: "chrome120",
  jsx: "automatic",
  jsxImportSource: "preact",
  minify: process.env.XMESH_UI_MINIFY === "1",
  sourcemap: false,
  legalComments: "none",
  logLevel: "warning",
  define: { "process.env.NODE_ENV": '"production"' },
});

for (const f of ["index.html", "styles.css"]) fs.copyFileSync(path.join(src, f), path.join(out, f));

fs.copyFileSync(path.join(root, "node_modules/@xterm/xterm/css/xterm.css"), path.join(out, "xterm.css"));
