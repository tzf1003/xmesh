import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import http from "node:http";
import type { Config, RunResult } from "../src/types.js";
import { defaults } from "../src/config.js";
import { Controller } from "../src/controller.js";

export function config(
  root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-test-")),
): Config {
  return {
    version: 1,
    state_dir: fs.realpathSync(root),
    environments: {},
    limits: { ...defaults },
  };
}
export async function site(): Promise<{
  url: string;
  close: () => Promise<void>;
  writes: () => number;
}> {
  let writes = 0;
  const html = `<!doctype html><html><body>
    <label>Username<input aria-label="Username"></label><label>Password<input type="password"></label>
    <button onclick="document.querySelector('[data-testid=result]').textContent=document.querySelector('input').value;fetch('/write',{method:'POST'})">Search</button>
    <div data-testid="result"></div><div class="duplicate">Same</div><div class="duplicate">Same</div>
    <input aria-label="Attachment" type="file"><a download="example.txt" href="/download">Download</a>
    <button onclick="window.open('/popup')">Popup</button><iframe src="/frame"></iframe>
  </body></html>`;
  const server = http.createServer((req, res) => {
    if (req.url === "/write") {
      writes++;
      res.end("ok");
      return;
    }
    if (req.url === "/download") {
      res.setHeader("Content-Type", "application/octet-stream");
      res.end("example evidence");
      return;
    }
    res.setHeader("Content-Type", "text/html");
    res.end(
      req.url === "/frame"
        ? "<button>Inside frame</button>"
        : req.url === "/popup"
          ? "<h1>Popup page</h1>"
          : html,
    );
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address() as { port: number };
  return {
    url: `http://127.0.0.1:${address.port}`,
    close: () =>
      new Promise((resolve, reject) =>
        server.close((e) => (e ? reject(e) : resolve())),
      ),
    writes: () => writes,
  };
}
export async function finished(
  controller: Controller,
  id: string,
  timeout = 15000,
): Promise<RunResult> {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const result = controller.result(id) as unknown as RunResult;
    if (!["accepted", "running"].includes(result.status)) return result;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error("run did not finish within deadline");
}
export function executable(): string | undefined {
  return process.env.XMESH_UI_TEST_BROWSER;
}
