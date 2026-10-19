import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Controller } from "../../src/controller.js";
import { cleanup, config, finished } from "../helpers.js";

test(
  "Electron dedicated instance navigates, confirms and captures evidence",
  { skip: !process.env.XMESH_UI_TEST_ELECTRON },
  async () => {
    const c = config(),
      app = fileURLToPath(
        new URL("../../../examples/electron/main.cjs", import.meta.url),
      );
    c.environments.electron = {
      driver: "electron",
      background_capable: true,
      executable: process.env.XMESH_UI_TEST_ELECTRON,
      args: [...(process.platform === "linux" ? ["--no-sandbox"] : []), app],
      fixtures: "mock",
      coverage: ["electron-renderer"],
    };
    const controller = new Controller(c);
    try {
      const opened = await controller.open("electron"),
        session = opened.session_id as string;
      const run = controller.submit({
        protocol_version: 1,
        session_id: session,
        request_id: "electron",
        steps: [
          { id: "nav", action: "click", target: { testId: "nav-settings" } },
          {
            id: "edit",
            action: "fill",
            target: { label: "Node name" },
            value: "mcp-node",
          },
          { id: "review", action: "click", target: { testId: "review" } },
          {
            id: "confirm",
            action: "assert",
            target: { testId: "confirm" },
            expect: { visible: true },
          },
          { id: "apply", action: "click", target: { testId: "confirm-ok" } },
          {
            id: "verify",
            action: "assert",
            target: { testId: "applied" },
            expect: { text: "Applied mcp-node" },
          },
          { id: "evidence", action: "screenshot" },
        ],
      });
      const result = await finished(controller, run.run_id, 45000);
      assert.equal(result.status, "passed", JSON.stringify(result));
      assert.ok(result.versions.electron);
      const snapshot = (await controller.snapshot(session)) as { aria: string };
      assert.match(snapshot.aria, /mcp-node/);
      assert.ok(result.artifacts.find((a) => a.mime_type === "image/png"));
      assert.throws(
        () =>
          controller.submit(
            {
              protocol_version: 1,
              session_id: session,
              request_id: "deny-script",
              code: "export default async()=>{}",
            },
            true,
          ),
        /disabled/,
      );
      await controller.closeSession(session);
      await controller.closeSession(session);
    } finally {
      await controller.close();
      cleanup(c);
    }
  },
);
