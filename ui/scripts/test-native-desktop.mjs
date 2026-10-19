import { _electron as electron } from "@playwright/test";
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const token = randomBytes(24).toString("hex");
const data = mkdtempSync(path.join(tmpdir(), "xmesh-native-fixture-"));
const args = [`--user-data-dir=${data}`, path.join(root, "ui/scripts/native-desktop-fixture.cjs")];
if (process.platform === "linux") args.unshift("--no-sandbox");
const app = await electron.launch({ args, env: { ...process.env, XMESH_DESKTOP_FIXTURE_TOKEN: token } });
try {
  const endpoint = await new Promise((resolve, reject) => {
    let output = "";
    const timer = setTimeout(() => reject(new Error("native fixture did not start")), 10000);
    app.process().stdout.on("data", (chunk) => { output += chunk; for (const line of output.split("\n")) { try { const v = JSON.parse(line); if (v.endpoint) { clearTimeout(timer); resolve(v.endpoint); return; } } catch {} } });
  });
  await (await app.firstWindow()).bringToFront();
  const exit = await new Promise((resolve) => {
    const child = spawn("go", ["test", "./internal/desktopbroker", "-run", "TestNative(InputFixture|UserIntervention)", "-v", "-count=1", "-timeout=90s"], { cwd: root, stdio: "inherit", env: { ...process.env, XMESH_TEST_COMPUTER_FIXTURE: endpoint, XMESH_TEST_COMPUTER_FIXTURE_TOKEN: token } });
    child.on("exit", (code) => resolve(code ?? 1));
    child.on("error", () => resolve(1));
  });
  if (exit !== 0) process.exitCode = exit;
} finally { await app.close(); rmSync(data, { recursive: true, force: true }); }
