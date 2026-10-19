import { build } from "esbuild";
import { fileURLToPath } from "node:url";
import * as path from "node:path";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
await build({ entryPoints: [path.join(root, "src/main/mcp-sdk.ts")], outfile: path.join(root, "dist/src/main/mcp-sdk.js"), bundle: true, platform: "node", format: "cjs", target: "node22", legalComments: "eof" });
