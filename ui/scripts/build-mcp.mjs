import { build } from "esbuild";
import { fileURLToPath } from "node:url";
import * as path from "node:path";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
// Main-process third-party libraries ship as compiled modules, without node_modules.
await Promise.all(["mcp-sdk", "kernel-install-options", "yaml"].map((name) => build({
  entryPoints: [path.join(root, `src/main/${name}.ts`)],
  outfile: path.join(root, `dist/src/main/${name}.js`),
  bundle: true, platform: "node", format: "cjs", target: "node22", legalComments: "eof",
})));
