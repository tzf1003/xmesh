import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Controller } from "../../src/controller.js";
import { cleanup, config, finished, executable } from "../helpers.js";
import { chromium } from "@playwright/test";

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
      mask: ['[data-testid="applied"]'],
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
      const screenshot = result.artifacts.find(
        (a) => a.mime_type === "image/png",
      )!;
      const encoded = controller.artifact(screenshot.id, 0, 262144)
        .data as string;
      const browser = await chromium.launch({
        executablePath: executable(),
        headless: true,
      });
      try {
        const page = await browser.newPage();
        const masked = await page.evaluate(async (data) => {
          const image = new Image();
          image.src = `data:image/png;base64,${data}`;
          await image.decode();
          const canvas = document.createElement("canvas");
          canvas.width = image.width;
          canvas.height = image.height;
          const context = canvas.getContext("2d")!;
          context.drawImage(image, 0, 0);
          const pixels = context.getImageData(
            0,
            0,
            image.width,
            image.height,
          ).data;
          let count = 0;
          for (let i = 0; i < pixels.length; i += 4)
            if (
              pixels[i] === 255 &&
              pixels[i + 1] === 0 &&
              pixels[i + 2] === 255
            )
              count++;
          return count;
        }, encoded);
        assert.ok(
          masked > 100,
          "configured screenshot mask is missing from captured bitmap",
        );
      } finally {
        await browser.close();
      }
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
