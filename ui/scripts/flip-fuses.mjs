// Flips (or with --verify, only checks) the Electron fuses on an Electron binary.
//
//   node scripts/flip-fuses.mjs --profile test                 flip node_modules/electron, then verify
//   node scripts/flip-fuses.mjs --profile release --binary X   flip a packaged app
//   node scripts/flip-fuses.mjs --verify --profile release --binary X
//
// --binary defaults to the Electron installed in node_modules.

import { FuseState, FuseV1Options, flipFuses, getCurrentFuseWire } from "@electron/fuses";
import { createRequire } from "node:module";
import { profiles } from "./fuses.config.mjs";

const args = process.argv.slice(2);
const flag = (name) => args.includes(`--${name}`);
const value = (name) => {
  const i = args.indexOf(`--${name}`);
  return i === -1 ? undefined : args[i + 1];
};

const profileName = value("profile") ?? "release";
const profile = profiles[profileName];
if (!profile) {
  console.error(`unknown profile ${profileName}; have ${Object.keys(profiles).join(", ")}`);
  process.exit(2);
}
const binary = value("binary") ?? createRequire(import.meta.url)("electron");

if (!flag("verify")) {
  await flipFuses(binary, {
    ...profile,
    // arm64 macOS refuses a binary whose signature no longer matches.
    resetAdHocDarwinSignature: process.platform === "darwin" && process.arch === "arm64",
  });
}

const wire = await getCurrentFuseWire(binary);
let bad = 0;
for (const [key, want] of Object.entries(profile)) {
  if (key === "version") continue;
  const name = FuseV1Options[key];
  const got = wire[key];
  const expected = want ? FuseState.ENABLE : FuseState.DISABLE;
  const okay = got === expected;
  if (!okay) bad++;
  console.log(`${okay ? "ok  " : "FAIL"} ${name} = ${want ? "enabled" : "disabled"}${okay ? "" : ` (found ${FuseState[got] ?? got})`}`);
}
if (bad > 0) {
  console.error(`${bad} fuse(s) do not match the ${profileName} profile`);
  process.exit(1);
}
console.log(`${profileName} fuses verified on ${binary}`);
