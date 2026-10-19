import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Controller } from "../../src/controller.js";
import { parse } from "yaml";
import { cleanup, config, finished } from "../helpers.js";

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
        script_enabled: true,
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
          "tray-menu-hook",
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
          timeout_ms: 120000,
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
            {
              id: "edit-allow",
              action: "click",
              target: { testId: "settings-allow-remove-0" },
            },
            {
              id: "review",
              action: "click",
              target: { testId: "settings-review" },
            },
            {
              id: "confirm",
              action: "assert",
              target: { testId: "confirm" },
              expect: { visible: true },
              timeout_ms: 30000,
            },
            { id: "save", action: "click", target: { testId: "confirm-ok" } },
            {
              id: "applied",
              action: "assert",
              target: { testId: "apply-applied" },
              expect: { visible: true },
              timeout_ms: 30000,
            },
          ],
        });
        const result = await finished(controller, request.run_id, 150000);
        if (result.status !== "passed") {
          const sessionDir = path.join(
            c.state_dir,
            "instances",
            controller.instance_id,
            "sessions",
            session,
          );
          for (const name of ["kernel.log", "panel-control-errors.log"]) {
            const log = path.join(sessionDir, name);
            if (fs.existsSync(log))
              process.stderr.write(fs.readFileSync(log, "utf8").slice(-8192));
          }
          process.stderr.write(
            JSON.stringify(await controller.snapshot(session)),
          );
        }
        assert.equal(result.status, "passed", JSON.stringify(result));
        assert.equal(result.fixtures, mode);
        if (mode === "real") {
          const saved = parse(
            fs.readFileSync(
              path.join(
                c.state_dir,
                "instances",
                controller.instance_id,
                "sessions",
                session,
                "kernel.json",
              ),
              "utf8",
            ),
          );
          assert.deepEqual(saved.agent.cwd_policy.allow, []);
        }
        const tray = controller.submit(
          {
            protocol_version: 1,
            session_id: session,
            request_id: "tray-script",
            code: fs.readFileSync(
              fileURLToPath(
                new URL("../../../examples/xmesh-tray.mjs", import.meta.url),
              ),
              "utf8",
            ),
          },
          true,
        );
        const trayResult = await finished(controller, tray.run_id, 30000);
        assert.equal(trayResult.status, "passed", JSON.stringify(trayResult));
      } finally {
        await controller.close();
        cleanup(c);
      }
    },
  );
