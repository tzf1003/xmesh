// The policy E2E fixture delegates local HuJSON operations to the real CLI.
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dir = path.join(ui, "build", "policy-test");
fs.mkdirSync(dir, { recursive: true });
const binary = path.join(dir, process.platform === "win32" ? "xmesh.exe" : "xmesh");
const result = spawnSync("go", ["build", "-o", binary, "./cmd/xmesh"], { cwd: path.dirname(ui), stdio: "inherit" });
if (result.error) throw result.error;
if (result.status !== 0) process.exit(result.status ?? 1);
