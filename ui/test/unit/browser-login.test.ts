import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { test } from "node:test";
import { isLoginUrl, runBrowserLogin } from "../../src/main/browser-login";

const control = "https://controlplane.tailscale.com";

test("only the chosen provider can request a browser authorization", () => {
  assert.ok(isLoginUrl("https://login.tailscale.com/a/challenge", control));
  assert.ok(isLoginUrl("https://headscale.example/register/node", "https://headscale.example"));
  for (const url of ["https://evil.example/a/x", "https://login.tailscale.com.evil/a/x", "https://user:pass@login.tailscale.com/a/x", "http://login.tailscale.com/a/x", "file:///etc/passwd", "https://login.tailscale.com/admin"]) {
    assert.equal(isLoginUrl(url, control), false, url);
  }
});

function launch(events: unknown[], exitCode = 0) {
  return () => spawn(process.execPath, ["-e", `for (const p of ${JSON.stringify(events)}) process.stdout.write(JSON.stringify(p) + '\\n'); process.exitCode = ${exitCode};`], { stdio: ["ignore", "pipe", "pipe"] });
}

test("browser challenge opens once and installation waits for ready and process exit", async () => {
  const url = "https://login.tailscale.com/a/test";
  const opened: string[] = [];
  await runBrowserLogin("/config", "/state", control, async (u) => { opened.push(u); }, launch([
    { state: "needs_login", url }, { state: "needs_login", url }, { state: "needs_approval" }, { state: "ready" },
  ]));
  assert.deepEqual(opened, [url]);
});

test("cancelled or invalid authorization stops setup", async () => {
  for (const events of [[{ state: "needs_approval" }], [{ state: "needs_login", url: "https://evil.example/a/x" }], [null]]) {
    await assert.rejects(runBrowserLogin("/config", "/state", control, async () => { assert.fail("invalid URL opened"); }, launch(events)));
  }
  await assert.rejects(runBrowserLogin("/config", "/state", control, async () => { throw new Error("browser missing"); }, launch([{ state: "needs_login", url: "https://login.tailscale.com/a/test" }, { state: "ready" }])));
  await assert.rejects(runBrowserLogin("/config", "/state", control, async () => {}, launch([{ state: "ready" }], 1)));
});
