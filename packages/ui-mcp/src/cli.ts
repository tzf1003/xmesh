#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { chromium } from "@playwright/test";
import { loadConfig } from "./config.js";
import { Controller } from "./controller.js";
import { gc } from "./artifacts.js";
import { failure, requireValue } from "./errors.js";
import { serve } from "./server.js";

const args = process.argv.slice(2),
  command = args[0];
const flag = (name: string) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 ? args[i + 1] : undefined;
};
try {
  if (command === "install-browser") {
    const cache = flag("cache");
    const cli = createRequire(import.meta.url).resolve("playwright/cli");
    const result = spawnSync(process.execPath, [cli, "install", "chromium"], {
      stdio: "inherit",
      env: {
        ...process.env,
        ...(cache ? { PLAYWRIGHT_BROWSERS_PATH: path.resolve(cache) } : {}),
      },
    });
    process.exitCode = result.status ?? 1;
  } else if (["serve", "doctor", "inspect", "gc"].includes(command ?? "")) {
    requireValue(flag("config"), "invalid_args", "--config FILE is required");
    const config = loadConfig(flag("config")!);
    if (command === "serve") await serve(config);
    else if (command === "gc")
      process.stdout.write(`${JSON.stringify(gc(config))}\n`);
    else if (command === "inspect") {
      const id = flag("run");
      requireValue(
        id && /^r_[a-f0-9-]+$/.test(id),
        "invalid_args",
        "--run RUN_ID is required",
      );
      const root = path.join(config.state_dir, "instances");
      let found = false;
      if (fs.existsSync(root))
        for (const i of fs
          .readdirSync(root, { withFileTypes: true })
          .filter((e) => e.isDirectory())) {
          const file = path.join(root, i.name, "runs", id, "result.json");
          if (fs.existsSync(file)) {
            process.stdout.write(`${fs.readFileSync(file, "utf8")}\n`);
            found = true;
            break;
          }
        }
      requireValue(found, "not_found", "run does not exist on this node");
    } else {
      const controller = new Controller(config);
      const value = controller.capabilities();
      const needsBrowser = Object.values(config.environments).some(
        (e) => e.driver === "web" && !e.executable,
      );
      const browserAvailable =
        !needsBrowser || fs.existsSync(chromium.executablePath());
      const checks = {
        ...value,
        node: process.version,
        chromium_installed: browserAvailable,
        state_writable: await fs.promises
          .access(config.state_dir, fs.constants.W_OK)
          .then(
            () => true,
            () => false,
          ),
      };
      await controller.close();
      process.stdout.write(`${JSON.stringify(checks, null, 2)}\n`);
      if (
        !browserAvailable ||
        (value.environments as any[]).some((e) => !e.preflight.ready)
      )
        process.exitCode = 1;
    }
  } else {
    process.stdout.write(
      "xmesh-ui-mcp <serve|doctor|inspect|gc> --config FILE\nxmesh-ui-mcp install-browser [--cache DIRECTORY]\n",
    );
    process.exitCode = command ? 2 : 0;
  }
} catch (error) {
  process.stderr.write(`${JSON.stringify({ error: failure(error) })}\n`);
  process.exitCode = 1;
}
