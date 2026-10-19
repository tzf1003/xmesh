import fs from "node:fs";
import path from "node:path";
import net from "node:net";
import { spawn } from "node:child_process";
import { pathToFileURL } from "node:url";

// Configure XMESH_UI_FIXTURE_MODULE for mock-kernel coverage, or
// XMESH_UI_KERNEL_BINARY for the real-kernel path. Paths belong to the node config.
export default async ({
  electronApp,
  sessionDir,
  environment,
  registerProcess,
}) => {
  const vars = environment.env ?? {},
    stateRoot = path.join(sessionDir, "state");
  // The fixture main process reports transport errors separately from renderer
  // console events. Retain a bounded diagnostic for native acceptance failures.
  const stderr = electronApp.process().stderr;
  let pending = "", diagnosticBytes = 0;
  const diagnostic = (chunk) => {
    pending = (pending + chunk.toString("utf8")).slice(-8192);
    const lines = pending.split(/\r?\n/);
    pending = lines.pop() ?? "";
    for (const line of lines) {
      if (!line.includes("test-control-error") || diagnosticBytes >= 8192) continue;
      const text = Buffer.from(line + "\n").subarray(0, 8192 - diagnosticBytes);
      fs.appendFileSync(path.join(sessionDir, "panel-control-errors.log"), text, { mode: 0o600 });
      diagnosticBytes += text.length;
    }
  };
  stderr?.on("data", diagnostic);
  let cleanup;
  const workspace = path.join(sessionDir, "workspace");
  fs.mkdirSync(workspace, { recursive: true });
  if (vars.XMESH_UI_FIXTURE_MODULE) {
    const { FakeKernel } = await import(
      pathToFileURL(vars.XMESH_UI_FIXTURE_MODULE).href
    );
    const kernel = await FakeKernel.start({
      pingMs: 300,
      capabilities: [
        "status",
        "events",
        "peers",
        "sessions",
        "drain",
        "config.read",
        "config.write",
        "pair",
        "pats",
        "ingress",
        "audit",
        "gateway",
      ],
    });
    kernel.panel.setContent(
      JSON.stringify({
        node: {
          name: `ui-test-${path.basename(sessionDir).slice(-8)}`,
          roles: ["agent", "gateway"],
        },
        mesh: { mode: "lan", lan: { mdns: true } },
        gateway: { listen: "127.0.0.1:8080" },
        agent: {
          default_cwd: workspace,
          cwd_policy: { allow: [workspace], deny: [] },
        },
      }),
    );
    kernel.writeManifest(stateRoot, "node-a");
    cleanup = () => kernel.stop();
  } else if (vars.XMESH_UI_KERNEL_BINARY) {
    const state = path.join(stateRoot, "node-a");
    fs.mkdirSync(state, { recursive: true });
    const listener = net.createServer();
    await new Promise((resolve) => listener.listen(0, "127.0.0.1", resolve));
    const port = listener.address().port;
    await new Promise((resolve) => listener.close(resolve));
    const config = path.join(sessionDir, "kernel.json");
    fs.writeFileSync(
      config,
      JSON.stringify({
        node: {
          name: `ui-test-${path.basename(sessionDir).slice(-8)}`,
          roles: ["agent", "gateway"],
        },
        mesh: { mode: "lan", lan: { mdns: true } },
        gateway: { listen: `127.0.0.1:${port}` },
        agent: {
          default_cwd: workspace,
          cwd_policy: { allow: [workspace] },
          authz: { "tag:peer": { tools: ["*"], backends: ["*"] } },
        },
      }),
    );
    const log = fs.openSync(path.join(sessionDir, "kernel.log"), "w", 0o600);
    const child = spawn(
      vars.XMESH_UI_KERNEL_BINARY,
      [
        "serve",
        "--local",
        "--config",
        config,
        "--state-dir",
        state,
        "--listen",
        "127.0.0.1:0",
      ],
      { stdio: ["ignore", log, log], windowsHide: true },
    );
    registerProcess?.(child.pid);
    fs.closeSync(log);
    cleanup = async () => {
      if (child.exitCode !== null) return;
      child.kill();
      await new Promise((resolve) => child.once("exit", resolve));
    };
  } else throw new Error("configure a fixture module or real kernel binary");
  try {
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      if (await electronApp.evaluate(() => !!globalThis.__xmeshUiTest)) break;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    await electronApp.evaluate(() => globalThis.__xmeshUiTest.showPanel());
    return async () => { stderr?.off("data", diagnostic); await cleanup(); };
  } catch (error) {
    stderr?.off("data", diagnostic);
    await cleanup();
    throw error;
  }
};
