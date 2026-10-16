// Copies the generated Control API types (api/control/v1/ts/control.ts, produced by
// scripts/gen-control-ts from docs/contracts/schemas/control-*.schema.json) into
// src/shared/generated/ so the UI project compiles on its own.
//
//   node scripts/sync-control-types.mjs          update the copy
//   node scripts/sync-control-types.mjs --check  fail if the copy is out of date (CI)

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const source = join(here, "..", "..", "api", "control", "v1", "ts", "control.ts");
const target = join(here, "..", "src", "shared", "generated", "control.ts");

const upstream = readFileSync(source, "utf8");
const header =
  "// Copy of api/control/v1/ts/control.ts. Do not edit: run `npm run sync-types` after the contract changes.\n";
const wanted = header + upstream;

if (process.argv.includes("--check")) {
  let have = "";
  try {
    have = readFileSync(target, "utf8");
  } catch {
    // missing counts as stale
  }
  if (have !== wanted) {
    console.error("src/shared/generated/control.ts is out of date with api/control/v1/ts/control.ts; run `npm run sync-types`");
    process.exit(1);
  }
  console.log("control types are in sync with the contract");
} else {
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, wanted);
  console.log(`wrote ${target}`);
}
