import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { Controller } from "../../src/controller.js";
import { cleanup, config, site, executable, finished } from "../helpers.js";

test("files, frames, popup selection, secrets and node resource limits", async () => {
  const web = await site(),
    c = config();
  c.limits.node_sessions = 1;
  const input = path.join(c.state_dir, "input");
  fs.mkdirSync(input);
  fs.writeFileSync(path.join(input, "sample.txt"), "sample");
  c.environments.web = {
    driver: "web",
    url: web.url,
    allowed_origins: [web.url],
    executable: executable(),
    upload_root: input,
    accounts: { test: { secrets: { username: "masked-user" } } },
    locators: { search: { role: "button", name: "Search" } },
  };
  const a = new Controller(c),
    b = new Controller(c);
  try {
    const opened = await a.open("web", "test");
    await assert.rejects(() => b.open("web"), /node session limit/);
    const run = a.submit({
      protocol_version: 1,
      session_id: opened.session_id as string,
      request_id: "extended",
      steps: [
        {
          id: "secret",
          action: "fill",
          target: { label: "Username" },
          secret: "username",
        },
        { id: "search", action: "click", target: { alias: "search" } },
        { id: "read", action: "read", target: { testId: "result" } },
        {
          id: "frame",
          action: "click",
          target: { frame: "iframe", role: "button", name: "Inside frame" },
        },
        {
          id: "upload",
          action: "upload",
          target: { label: "Attachment" },
          file: "sample.txt",
        },
        {
          id: "download",
          action: "download",
          target: { role: "link", name: "Download" },
        },
        {
          id: "popup",
          action: "click",
          target: { role: "button", name: "Popup" },
        },
        { id: "wait-popup", action: "wait_page" },
        {
          id: "popup-text",
          action: "assert",
          target: { role: "heading", name: "Popup page" },
          expect: { visible: true },
        },
      ],
    });
    const result = await finished(a, run.run_id);
    assert.equal(result.status, "passed", JSON.stringify(result));
    assert.ok(!JSON.stringify(result).includes("masked-user"));
    assert.ok(result.artifacts.find((x) => x.name === "example.txt"));
    await a.closeSession(opened.session_id as string);
    const next = await b.open("web");
    await b.closeSession(next.session_id as string);
  } finally {
    await a.close();
    await b.close();
    await web.close();
    cleanup(c);
  }
});
