import { spawnSync } from "node:child_process";
import fs from "node:fs";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import path from "node:path";
const root = fileURLToPath(new URL("../", import.meta.url));
const result = spawnSync(
  process.platform === "win32" ? "npm.cmd" : "npm",
  ["pack", "--json"],
  { cwd: root, encoding: "utf8", shell: process.platform === "win32" },
);
if (result.status !== 0) throw new Error(result.stderr || result.stdout);
const text = result.stdout;
const packages = JSON.parse(text.slice(text.indexOf("[")));
for (const item of packages) {
  const hash = createHash("sha256")
    .update(fs.readFileSync(path.join(root, item.filename)))
    .digest("hex");
  fs.writeFileSync(
    path.join(root, `${item.filename}.sha256`),
    `${hash}  ${item.filename}\n`,
  );
  process.stdout.write(`${item.filename}\n${hash}\n`);
}
