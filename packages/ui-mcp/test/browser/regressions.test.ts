import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import http from "node:http";
import { Controller } from "../../src/controller.js";
import { Driver } from "../../src/driver.js";
import { cleanup, config, site, executable, finished } from "../helpers.js";

async function until(check: () => boolean, timeout = 10000) {
  const end = Date.now() + timeout;
  while (!check()) {
    if (Date.now() >= end) throw new Error("condition timed out");
    await new Promise((r) => setTimeout(r, 20));
  }
}

test("unexpected worker exits preserve completed steps and flag uncertain writes", async () => {
  const web = await site(),
    c = config();
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
    const run = controller.submit({
      protocol_version: 1,
      session_id: opened.session_id as string,
      request_id: "worker-crash",
      steps: [
        {
          id: "write",
          action: "click",
          target: { role: "button", name: "Search" },
        },
        {
          id: "waiting",
          action: "wait",
          target: { text: "never" },
          expect: { visible: true },
        },
      ],
    });
    await until(
      () =>
        (controller.result(run.run_id) as any).steps[1].status === "started" &&
        web.writes() === 1,
    );
    const session = (controller as any).sessions.get(opened.session_id);
    session.child.kill("SIGKILL");
    const result = await finished(controller, run.run_id);
    assert.equal(result.status, "interrupted");
    assert.equal(result.outcome_unknown, true);
    assert.equal(result.evidence_incomplete, true);
    assert.equal(result.steps[0]!.status, "passed");
    assert.equal(result.steps[1]!.status, "started");
    const persisted = JSON.parse(
      fs.readFileSync(
        path.join(
          c.state_dir,
          "instances",
          controller.instance_id,
          "runs",
          run.run_id,
          "result.json",
        ),
        "utf8",
      ),
    );
    assert.equal(persisted.outcome_unknown, true);
    await controller.closeSession(opened.session_id as string);
    // Browser death can deliver a failed step before the worker reports the
    // application_exited reply; that completed journal entry is still uncertain.
    const closedApp = await controller.open("web");
    const closingRun = controller.submit({
      protocol_version: 1,
      session_id: closedApp.session_id as string,
      request_id: "application-close",
      steps: [
        {
          id: "write",
          action: "click",
          target: { role: "button", name: "Search" },
        },
        {
          id: "waiting",
          action: "fill",
          target: { label: "absent-field" },
          value: "pending",
        },
      ],
    });
    await until(
      () =>
        (controller.result(closingRun.run_id) as any).steps[1].status ===
        "started",
    );
    const closingSession = (controller as any).sessions.get(
      closedApp.session_id,
    );
    const browserProcess = closingSession.processes.find(
      (owned: any) => owned.pid !== closingSession.child.pid,
    );
    assert.ok(browserProcess);
    process.kill(browserProcess.pid, "SIGKILL");
    const appResult = await finished(controller, closingRun.run_id);
    assert.equal(
      appResult.status,
      "interrupted",
      JSON.stringify(appResult.error),
    );
    assert.equal(appResult.steps[1]!.status, "failed");
    assert.equal(appResult.outcome_unknown, true);
    assert.equal(appResult.evidence_incomplete, true);
    await controller.closeSession(closedApp.session_id as string);
    // A script can die after completing its last instrumented phase.
    const scripted = await controller.open("web");
    const script = controller.submit(
      {
        protocol_version: 1,
        session_id: scripted.session_id as string,
        request_id: "script-crash",
        code: `export default async ({page,step}) => { await step("write", () => page.getByRole("button",{name:"Search"}).click()); process.exit(7); }`,
      },
      true,
    );
    const scriptResult = await finished(controller, script.run_id);
    assert.equal(scriptResult.status, "interrupted");
    assert.equal(scriptResult.outcome_unknown, true);
    assert.equal(scriptResult.evidence_incomplete, true);
  } finally {
    await controller.close();
    await web.close();
    cleanup(c);
  }
});

test("response assertions bind to the latest action and selected page", async () => {
  let writes = 0,
    redirects = 0;
  const server = http.createServer((req, res) => {
    if (req.url === "/redirect-source") {
      redirects++;
      setTimeout(() => {
        res.writeHead(302, { Location: "/lineage" });
        res.end();
      }, 150);
    } else if (req.url === "/lineage") {
      setTimeout(
        () => {
          res.statusCode = req.method === "POST" ? 500 : 200;
          res.end("lineage");
        },
        req.method === "POST" ? 400 : 0,
      );
    } else if (req.url === "/save") {
      const count = ++writes;
      setTimeout(
        () => {
          res.statusCode = count === 1 ? 200 : 500;
          res.end("done");
        },
        count === 1 ? 10 : 300,
      );
    } else {
      res.setHeader("Content-Type", "text/html");
      res.end(
        `<button onclick="fetch('/save',{method:'POST'})">Save</button><button onclick="fetch('/lineage',{method:'POST'})">Lineage</button>`,
      );
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as any).port}`,
    c = config();
  const driver = new Driver(
    {
      config: c,
      environment: {
        driver: "web",
        url,
        allowed_origins: [url],
        executable: executable(),
      },
      session_dir: c.state_dir,
    },
    () => {},
  );
  try {
    await driver.open();
    driver.beginRun();
    const click = {
      id: "save",
      action: "click" as const,
      target: { role: "button", name: "Save" },
    };
    await driver.step(click, c.state_dir);
    assert.deepEqual(
      await driver.step(
        { id: "first", action: "response", value: "/save", status: 200 },
        c.state_dir,
      ),
      { status: 200 },
    );
    await driver.step(click, c.state_dir);
    await assert.rejects(
      driver.step(
        { id: "second", action: "response", value: "/save", status: 200 },
        c.state_dir,
      ),
      /status mismatch/,
    );
    // Fast responses remain usable by repeated assertions after the same action.
    assert.deepEqual(
      await driver.step(
        {
          id: "second-status",
          action: "response",
          value: "/save",
          status: 500,
        },
        c.state_dir,
      ),
      { status: 500 },
    );
    // An old request arriving during a new action must not satisfy that action.
    await driver.page.evaluate(() => {
      void fetch("/save", { method: "POST" });
    });
    await until(() => writes === 3);
    await driver.step(
      { id: "unrelated", action: "hover", target: click.target },
      c.state_dir,
    );
    await assert.rejects(
      driver.step(
        {
          id: "late",
          action: "response",
          value: "/save",
          status: 500,
          timeout_ms: 500,
        },
        c.state_dir,
      ),
      /Timeout/,
    );
    // Redirect hops inherit the earlier action's request lineage.
    await driver.page.evaluate(() => {
      void fetch("/redirect-source");
    });
    await until(() => redirects === 1);
    await driver.step(
      {
        id: "lineage-click",
        action: "click",
        target: { role: "button", name: "Lineage" },
      },
      c.state_dir,
    );
    await assert.rejects(
      driver.step(
        {
          id: "lineage-response",
          action: "response",
          value: "/lineage",
          status: 200,
        },
        c.state_dir,
      ),
      /status mismatch/,
    );
    // Responses on a different page cannot satisfy the selected page's assertion.
    const other = await driver.context.newPage();
    await other.goto(url);
    await other.evaluate(() => fetch("/save", { method: "POST" }));
    await assert.rejects(
      driver.step(
        {
          id: "other",
          action: "response",
          value: "/save",
          status: 500,
          timeout_ms: 100,
        },
        c.state_dir,
      ),
      /Timeout/,
    );
  } finally {
    await driver.close();
    await new Promise<void>((r) => server.close(() => r()));
    cleanup(c);
  }
});

test("navigation policy allows subresources and denies off-origin documents", async () => {
  let hits = 0,
    frameRedirects = 0,
    posts = 0,
    finalPosts = 0;
  const outsideRequests: string[] = [];
  const outside = http.createServer((req, res) => {
    const destination = req.headers["sec-fetch-dest"];
    outsideRequests.push(`${req.method} ${req.url} ${destination ?? "unknown"}`);
    // Subresources (including a popup's favicon) remain permitted. Count only
    // document navigations when checking the origin restriction.
    // Unclassified requests also count, so missing metadata fails closed.
    if (destination !== "empty" && destination !== "image") hits++;
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.end("outside");
  });
  await new Promise<void>((r) => outside.listen(0, "127.0.0.1", r));
  const outsideUrl = `http://127.0.0.1:${(outside.address() as any).port}`;
  const server = http.createServer((req, res) => {
    if (req.url === "/frame-redirect") {
      frameRedirects++;
      res.writeHead(302, { Location: outsideUrl });
      res.end();
    } else if (req.url === "/post") {
      posts++;
      assert.equal(req.method, "POST");
      res.writeHead(307, { Location: "/post-final" });
      res.end();
    } else if (req.url === "/post-final") {
      finalPosts++;
      assert.equal(req.method, "POST");
      let body = "";
      req.on("data", (data) => {
        body += data;
      });
      req.on("end", () => {
        res.setHeader("Content-Type", "text/html");
        res.end(`<h1>${body}</h1>`);
      });
    } else if (req.url === "/chain") {
      res.writeHead(302, { Location: "/redirect" });
      res.end();
    } else if (req.url === "/good") {
      res.writeHead(302, { Location: "/" });
      res.end();
    } else if (req.url === "/redirect") {
      res.writeHead(302, { Location: outsideUrl });
      res.end();
    } else {
      res.setHeader("Content-Type", "text/html");
      res.end("<h1>allowed</h1>");
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as any).port}`,
    c = config();
  const driver = new Driver(
    {
      config: c,
      environment: {
        driver: "web",
        url,
        allowed_origins: [url, url.replace("127.0.0.1", "localhost")],
        executable: executable(),
      },
      session_dir: c.state_dir,
    },
    () => {},
  );
  try {
    await driver.open();
    assert.equal(
      await driver.page.evaluate(
        async (address) => (await fetch(address)).text(),
        outsideUrl,
      ),
      "outside",
    );
    const allowedHits = hits;
    // Image requests, such as a popup's favicon, are permitted subresources.
    // Wait for completion before testing the document-navigation boundary.
    await driver.page.evaluate((address) => new Promise<void>((resolve) => {
      const image = new Image();
      image.onload = image.onerror = () => resolve();
      image.src = address + "/fixture-icon";
      document.body.append(image);
    }), outsideUrl);
    assert.ok(outsideRequests.some(request => request.includes("/fixture-icon image")), JSON.stringify(outsideRequests));
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    const forbidden = await driver.context.newPage();
    await assert.rejects(forbidden.goto(outsideUrl), /ERR_BLOCKED_BY_CLIENT/);
    await forbidden.close();
    await driver.page.evaluate((address) => {
      const frame = document.createElement("iframe");
      frame.src = address;
      document.body.append(frame);
    }, outsideUrl);
    await new Promise((r) => setTimeout(r, 100));
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    const redirect = await driver.context.newPage();
    await assert.rejects(
      redirect.goto(`${url}/redirect`),
      /ERR_BLOCKED_BY_CLIENT/,
    );
    await redirect.close();
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    const chain = await driver.context.newPage();
    await assert.rejects(chain.goto(`${url}/chain`), /ERR_BLOCKED_BY_CLIENT/);
    await chain.close();
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    const good = await driver.context.newPage();
    await good.goto(`${url}/good`);
    assert.equal(good.url(), `${url}/`);
    assert.equal(await good.locator("h1").innerText(), "allowed");
    // UI-created popups must be guarded before their initial request.
    const popupEvent = driver.page.waitForEvent("popup");
    await driver.page.evaluate(
      (address) => window.open(address),
      `${url}/chain`,
    );
    const popup = await popupEvent;
    await popup.waitForLoadState().catch(() => {});
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    await popup.close();
    const allowedPopupEvent = driver.page.waitForEvent("popup");
    await driver.page.evaluate(
      (address) => window.open(address),
      `${url}/good`,
    );
    const allowedPopup = await allowedPopupEvent;
    await allowedPopup.waitForLoadState();
    assert.equal(await allowedPopup.locator("h1").innerText(), "allowed");
    await allowedPopup.close();
    // A cross-site frame creates a separate Chromium target.
    await driver.page.evaluate(
      (address) => {
        const frame = document.createElement("iframe");
        frame.src = address;
        document.body.append(frame);
      },
      `${url.replace("127.0.0.1", "localhost")}/frame-redirect`,
    );
    await until(() => frameRedirects === 1);
    await new Promise((r) => setTimeout(r, 100));
    assert.equal(hits, allowedHits, JSON.stringify(outsideRequests));
    await good.evaluate((address) => {
      const form = document.createElement("form");
      form.method = "POST";
      form.action = address;
      const input = document.createElement("input");
      input.name = "value";
      input.value = "once";
      form.append(input);
      document.body.append(form);
      form.submit();
    }, `${url}/post`);
    await good.waitForURL(`${url}/post-final`);
    assert.equal(await good.locator("h1").innerText(), "value=once");
    assert.equal(posts, 1);
    assert.equal(finalPosts, 1);
  } finally {
    await driver.close();
    await new Promise<void>((r) => server.close(() => r()));
    await new Promise<void>((r) => outside.close(() => r()));
    cleanup(c);
  }
});

test("default account evidence omits trace and redacts configured secrets", async () => {
  const web = await site(),
    c = config();
  const secret = "synthetic-account-secret-4711";
  c.environments.web = {
    driver: "web",
    url: web.url,
    allowed_origins: [web.url],
    executable: executable(),
    accounts: { test: { secrets: { user: secret } } },
  };
  const controller = new Controller(c);
  try {
    const opened = await controller.open("web", "test");
    const submitted = controller.submit({
      protocol_version: 1,
      session_id: opened.session_id as string,
      request_id: "default-evidence",
      steps: [
        {
          id: "fill",
          action: "fill",
          target: { label: "Username" },
          secret: "user",
        },
        {
          id: "write",
          action: "click",
          target: { role: "button", name: "Search" },
        },
        { id: "read", action: "read", target: { testId: "result" } },
      ],
    });
    const result = await finished(controller, submitted.run_id);
    assert.equal(result.status, "passed");
    assert.equal(
      result.artifacts.some((a) => a.name === "trace.zip"),
      false,
    );
    assert.equal(JSON.stringify(result).includes(secret), false);
    for (const artifact of result.artifacts) {
      const bytes = controller.artifact(artifact.id);
      assert.equal(
        Buffer.from(bytes.data as string, "base64").includes(
          Buffer.from(secret),
        ),
        false,
      );
    }
    const root = path.join(
      c.state_dir,
      "instances",
      controller.instance_id,
      "runs",
      submitted.run_id,
    );
    assert.equal(fs.existsSync(path.join(root, "trace.zip")), false);
    for (const file of fs.readdirSync(root))
      assert.equal(
        fs.readFileSync(path.join(root, file)).includes(Buffer.from(secret)),
        false,
        file,
      );
  } finally {
    await controller.close();
    await web.close();
    cleanup(c);
  }
});
