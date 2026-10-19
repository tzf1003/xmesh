import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { Controller } from "../../src/controller.js";
import { config, site, executable, finished } from "../helpers.js";

test("abrupt backend death terminates its test processes and marks the retained run interrupted", async () => {
  const web = await site(),
    c = config(),
    file = path.join(c.state_dir, "config.json");
  fs.writeFileSync(
    file,
    JSON.stringify({
      version: 1,
      state_dir: c.state_dir,
      environments: {
        web: {
          driver: "web",
          url: web.url,
          script_enabled: true,
          executable: executable(),
        },
      },
    }),
  );
  const client = new Client({ name: "crash-test", version: "1" });
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: [
      fileURLToPath(new URL("../../src/cli.js", import.meta.url)),
      "serve",
      "--config",
      file,
    ],
    stderr: "pipe",
  });
  try {
    await client.connect(transport);
    const opened = (
      await client.callTool({
        name: "open",
        arguments: { environment_id: "web" },
      })
    ).structuredContent as any;
    const run = (
      await client.callTool({
        name: "script",
        arguments: {
          protocol_version: 1,
          session_id: opened.session_id,
          request_id: "crash",
          code: "export default async()=>{await new Promise(()=>{})}",
        },
      })
    ).structuredContent as any;
    const resultPath = path.join(
      c.state_dir,
      "instances",
      opened.instance_id,
      "runs",
      run.run_id,
      "result.json",
    );
    await new Promise((resolve) => setTimeout(resolve, 100));
    process.kill(transport.pid!, "SIGKILL");
    const deadline = Date.now() + 10000;
    let result: any;
    while (Date.now() < deadline) {
      result = JSON.parse(fs.readFileSync(resultPath, "utf8"));
      if (result.status === "interrupted") break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.equal(result.status, "interrupted");
    assert.equal(result.outcome_unknown, true);
    assert.ok(result.evidence_incomplete);
  } finally {
    await client.close().catch(() => {});
    await web.close();
    fs.rmSync(c.state_dir, { recursive: true, force: true });
  }
});

test("evidence quota stops a running script and preserves its diagnostic", async () => {
  const web = await site(),
    c = config();
  c.limits.run_bytes = 16384;
  c.environments.web = {
    driver: "web",
    url: web.url,
    allowed_origins: [web.url],
    executable: executable(),
    script_enabled: true,
  };
  const controller = new Controller(c);
  try {
    const opened = await controller.open("web");
    const run = controller.submit(
      {
        protocol_version: 1,
        session_id: opened.session_id as string,
        request_id: "quota",
        code: 'import fs from "node:fs";import {fileURLToPath} from "node:url";export default async()=>{fs.writeFileSync(fileURLToPath(new URL("large.bin",import.meta.url)),Buffer.alloc(32768));await new Promise(()=>{})}',
      },
      true,
    );
    const result = await finished(controller, run.run_id, 15000);
    assert.equal(result.status, "failed");
    assert.equal(result.error?.code, "evidence_quota");
    assert.equal(result.evidence_incomplete, true);
  } finally {
    await controller.close();
    await web.close();
    fs.rmSync(c.state_dir, { recursive: true, force: true });
  }
});
