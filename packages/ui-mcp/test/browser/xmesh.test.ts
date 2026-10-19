import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Controller } from "../../src/controller.js";
import { config, finished } from "../helpers.js";

for (const mode of ["mock", "real"] as const)
  test(
    `xmesh Electron panel with ${mode} kernel`,
    {
      skip:
        !process.env.XMESH_UI_TEST_APP ||
        !process.env.XMESH_UI_TEST_ELECTRON ||
        (mode === "real" && !process.env.XMESH_UI_TEST_KERNEL),
    },
    async () => {
      const c = config(),
        app = process.env.XMESH_UI_TEST_APP!;
      c.environments.xmesh = {
        driver: "electron",
        background_capable: true,
        executable: process.env.XMESH_UI_TEST_ELECTRON,
        args: [...(process.platform === "linux" ? ["--no-sandbox"] : []), app],
        env: {
          XMESH_UI_TEST: "1",
          XMESH_UI_LANG: "en",
          ...(mode === "mock"
            ? {
                XMESH_UI_FIXTURE_MODULE: path.join(
                  app,
                  "dist/testserver/server.js",
                ),
              }
            : { XMESH_UI_KERNEL_BINARY: process.env.XMESH_UI_TEST_KERNEL! }),
        },
        init_module: fileURLToPath(
          new URL("../../../examples/xmesh-init.mjs", import.meta.url),
        ),
        fixtures: mode,
        coverage: [
          "electron-renderer",
          mode === "mock" ? "mock-kernel" : "real-kernel",
          "panel-open-hook",
        ],
      };
      const controller = new Controller(c);
      try {
        const opened = await controller.open("xmesh"),
          session = opened.session_id as string;
        const request = controller.submit({
          protocol_version: 1,
          session_id: session,
          request_id: "panel",
          timeout_ms: 30000,
          steps: [
            {
              id: "connected",
              action: "assert",
              target: {
                css: '[data-testid="state"][data-state="ok"], [data-testid="state"][data-state="degraded"]',
              },
              expect: { visible: true },
              timeout_ms: 20000,
            },
            {
              id: "ready",
              action: "assert",
              target: { testId: "nav-settings" },
              expect: { visible: true },
              timeout_ms: 20000,
            },
            {
              id: "settings",
              action: "click",
              target: { testId: "nav-settings" },
            },
            {
              id: "page",
              action: "assert",
              target: { testId: "page-settings" },
              expect: { visible: true },
            },
            { id: "capture", action: "screenshot" },
          ],
        });
        const result = await finished(controller, request.run_id, 40000);
        if (result.status !== "passed") {
          const log = path.join(
            c.state_dir,
            "instances",
            controller.instance_id,
            "sessions",
            session,
            "kernel.log",
          );
          if (fs.existsSync(log))
            process.stderr.write(fs.readFileSync(log, "utf8").slice(-8192));
          process.stderr.write(
            JSON.stringify(await controller.snapshot(session)),
          );
        }
        assert.equal(result.status, "passed", JSON.stringify(result));
        assert.equal(result.fixtures, mode);
      } finally {
        await controller.close();
        fs.rmSync(c.state_dir, { recursive: true, force: true });
      }
    },
  );
