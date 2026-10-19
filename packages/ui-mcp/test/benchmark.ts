import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { config, site, executable } from "./helpers.js";

const c = config(),
  web = await site(),
  file = path.join(c.state_dir, "env.json");
fs.writeFileSync(
  file,
  JSON.stringify({
    version: 1,
    state_dir: c.state_dir,
    environments: {
      web: { driver: "web", url: web.url, executable: executable() },
    },
  }),
);
const client = new Client({ name: "benchmark", version: "1" });
const transport = new StdioClientTransport({
  command: process.execPath,
  args: [
    fileURLToPath(new URL("../src/cli.js", import.meta.url)),
    "serve",
    "--config",
    file,
  ],
  stderr: "inherit",
});
const browser = await chromium.launch({
  executablePath: executable(),
  headless: true,
});
let calls = 0,
  returnedBytes = 0;
async function call(name: string, args: Record<string, unknown>): Promise<any> {
  calls++;
  const result = await client.callTool({ name, arguments: args });
  returnedBytes += JSON.stringify(result).length;
  if (result.isError) throw new Error(JSON.stringify(result));
  return result.structuredContent;
}
async function wait(id: string): Promise<void> {
  while (true) {
    const result = await call("result", { run_id: id });
    if (result.status === "passed") return;
    if (!["accepted", "running"].includes(result.status))
      throw new Error(JSON.stringify(result));
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
const rows: Array<{
  mode: string;
  samples_ms: number[];
  calls: number;
  returned_bytes: number;
}> = [];
try {
  await client.connect(transport);
  const opened = await call("open", { environment_id: "web" });
  const page = await browser.newPage();
  await page.goto(web.url);
  const steps = Array.from({ length: 30 }, (_, i) => ({
    id: `fill-${i}`,
    action: "fill",
    target: { label: "Username" },
    value: `value-${i}`,
  }));
  let sequence = 0;
  for (const mode of ["direct-playwright", "mcp-per-step", "mcp-batch"]) {
    const row = {
      mode,
      samples_ms: [] as number[],
      calls: 0,
      returned_bytes: 0,
    };
    const beforeCalls = calls,
      beforeBytes = returnedBytes;
    for (let round = -1; round < 20; round++) {
      const start = performance.now();
      if (mode === "direct-playwright")
        for (const step of steps)
          await page.getByLabel("Username").fill(step.value);
      else if (mode === "mcp-batch") {
        const run = await call("run", {
          protocol_version: 1,
          session_id: opened.session_id,
          request_id: `bench-${sequence++}`,
          steps,
        });
        await wait(run.run_id);
      } else
        for (const step of steps) {
          const run = await call("run", {
            protocol_version: 1,
            session_id: opened.session_id,
            request_id: `bench-${sequence++}`,
            steps: [step],
          });
          await wait(run.run_id);
        }
      if (round >= 0) row.samples_ms.push(performance.now() - start);
    }
    row.calls = calls - beforeCalls;
    row.returned_bytes = returnedBytes - beforeBytes;
    rows.push(row);
  }
  const percentile = (samples: number[], p: number) =>
    [...samples].sort((a, b) => a - b)[Math.ceil(samples.length * p) - 1]!;
  const output = {
    date: new Date().toISOString(),
    node: process.version,
    browser: browser.version(),
    rounds: 20,
    steps: 30,
    transport: "local stdio MCP",
    warmup: 1,
    results: rows.map((row) => ({
      ...row,
      median_ms: percentile(row.samples_ms, 0.5),
      p95_ms: percentile(row.samples_ms, 0.95),
      failures: 0,
    })),
    batch_budget_ms: percentile(rows[0]!.samples_ms, 0.95) * 1.25 + 250,
    batch_passed:
      percentile(rows[2]!.samples_ms, 0.95) <=
      percentile(rows[0]!.samples_ms, 0.95) * 1.25 + 250,
  };
  fs.writeFileSync("benchmark-result.json", JSON.stringify(output, null, 2));
  process.stdout.write(`${JSON.stringify(output, null, 2)}\n`);
  if (!output.batch_passed) process.exitCode = 1;
} finally {
  await client.close();
  await browser.close();
  await web.close();
  fs.rmSync(c.state_dir, { recursive: true, force: true });
}
