import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { config } from "./helpers.js";

test("stdio MCP lists tools without launching a browser and closes on EOF", async () => {
  const c = config(),
    file = path.join(c.state_dir, "config.json");
  fs.writeFileSync(
    file,
    JSON.stringify({
      version: 1,
      state_dir: c.state_dir,
      environments: { web: { driver: "web", url: "http://127.0.0.1:1" } },
    }),
  );
  const client = new Client({ name: "ui-test", version: "1" });
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: [
      process.env.XMESH_UI_TEST_INSTALLED_CLI ??
        fileURLToPath(new URL("../src/cli.js", import.meta.url)),
      "serve",
      "--config",
      file,
    ],
    stderr: "pipe",
  });
  try {
    await client.connect(transport);
    const tools = await client.listTools();
    assert.ok(tools.tools.find((t) => t.name === "run"));
    assert.ok(!tools.tools.find((t) => t.name === "script"));
    const result = await client.callTool({
      name: "capabilities",
      arguments: {},
    });
    assert.equal((result.structuredContent as any).protocol_version, 1);
    const bad = await client.callTool({
      name: "open",
      arguments: { environment_id: "missing" },
    });
    assert.equal(bad.isError, true);
    const invalid = await client.callTool({
      name: "run",
      arguments: { steps: [] },
    });
    assert.equal(invalid.isError, true);
  } finally {
    await client.close();
    fs.rmSync(c.state_dir, { recursive: true, force: true });
  }
});
