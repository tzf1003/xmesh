// Electron hardening requirements that can be checked without starting Electron.
// The running-app side is in test/e2e/panel-security.spec.ts.

import assert from "node:assert/strict";
import * as path from "node:path";
import { test } from "node:test";
import { isTrustedSender } from "../../src/main/ipc";
import { PANEL_CSP, isPanelUrl, mimeFor, panelHeaders, panelWebPreferences, parseCsp, resolvePanelFile } from "../../src/main/policy";
import { EXTERNAL_LINKS } from "../../src/shared/ipc";

test("panel web preferences pin isolation, sandbox and no Node", () => {
  const p = panelWebPreferences("/x/preload.js", true);
  assert.equal(p.contextIsolation, true);
  assert.equal(p.sandbox, true);
  assert.equal(p.nodeIntegration, false);
  assert.equal(p.nodeIntegrationInWorker, false);
  assert.equal(p.nodeIntegrationInSubFrames, false);
  assert.equal(p.webSecurity, true);
  assert.equal(p.allowRunningInsecureContent, false);
  assert.equal(p.experimentalFeatures, false);
  assert.equal(p.webviewTag, false);
  assert.equal(p.preload, "/x/preload.js");
  assert.equal(p.devTools, false, "devtools are off in packaged builds");
  assert.equal(panelWebPreferences("/x/preload.js", false).devTools, true);
});

test("CSP: nothing remote, no inline code, no eval, no network from the page, no framing", () => {
  const csp = parseCsp(PANEL_CSP);
  assert.deepEqual(csp.get("default-src"), ["'none'"]);
  assert.deepEqual(csp.get("script-src"), ["'self'"]);
  assert.deepEqual(csp.get("style-src"), ["'self'"]);
  assert.deepEqual(csp.get("connect-src"), ["'none'"]);
  assert.deepEqual(csp.get("object-src"), ["'none'"]);
  assert.deepEqual(csp.get("base-uri"), ["'none'"]);
  assert.deepEqual(csp.get("form-action"), ["'none'"]);
  assert.deepEqual(csp.get("frame-ancestors"), ["'none'"]);
  assert.doesNotMatch(PANEL_CSP, /unsafe-inline|unsafe-eval|https?:|\*/);
  for (const [directive, sources] of csp) {
    for (const s of sources) assert.ok(["'none'", "'self'", "data:"].includes(s), `${directive} allows ${s}`);
  }
  assert.equal(panelHeaders("text/html")["Content-Security-Policy"], PANEL_CSP);
  assert.equal(panelHeaders("text/html")["X-Content-Type-Options"], "nosniff");
});

test("the placeholder page ships its own CSP that is no looser than the header", async () => {
  const fs = await import("node:fs");
  const html = fs.readFileSync(path.join(__dirname, "..", "..", "..", "static", "placeholder-panel", "index.html"), "utf8");
  const m = /Content-Security-Policy"\s+content="([^"]+)"/.exec(html);
  assert.ok(m, "meta CSP present");
  assert.doesNotMatch(m[1]!, /unsafe-inline|unsafe-eval|https?:|\*/);
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>/, "no inline scripts");
  assert.doesNotMatch(html, /\son\w+\s*=/i, "no inline event handlers");
});

test("only the app://panel origin counts as the panel", () => {
  assert.equal(isPanelUrl("app://panel/index.html"), true);
  assert.equal(isPanelUrl("app://panel"), true);
  for (const bad of ["app://other/index.html", "https://panel/", "file:///x/index.html", "about:blank", "app://panel.evil/", "", "not a url", "app://user@panel.evil/"]) {
    assert.equal(isPanelUrl(bad), false, bad);
  }
});

test("resolvePanelFile stays inside the root", () => {
  const root = path.resolve("/srv/panel");
  assert.equal(resolvePanelFile(root, "app://panel/"), path.join(root, "index.html"));
  assert.equal(resolvePanelFile(root, "app://panel/assets/a.js"), path.join(root, "assets", "a.js"));
  // The URL parser folds dot segments before we see them, so these land inside the root...
  for (const dots of ["app://panel/../secret", "app://panel/%2e%2e/secret", "app://panel/a/../../secret", "app://panel/%2fetc/passwd"]) {
    const r = resolvePanelFile(root, dots);
    assert.ok(r === undefined || r.startsWith(root + path.sep), `${dots} -> ${r}`);
  }
  // ...and everything that could smuggle a separator or another origin is refused outright.
  for (const bad of [
    "app://panel/a%5c..%5csecret",
    "app://panel/a%00.js",
    "app://panel/%zz",
    "app://evil/index.html",
    "https://panel/index.html",
    "not a url",
  ]) {
    assert.equal(resolvePanelFile(root, bad), undefined, bad);
  }
});

test("only known file types are served", () => {
  assert.equal(mimeFor("a.js"), "text/javascript; charset=utf-8");
  assert.equal(mimeFor("a.html"), "text/html; charset=utf-8");
  assert.equal(mimeFor("a.exe"), undefined);
  assert.equal(mimeFor("noext"), undefined);
});

test("IPC sender trust: top frame of the panel window on the panel origin, nothing else", () => {
  const top = { url: "app://panel/index.html", parent: null };
  assert.equal(isTrustedSender(top, true), true);
  assert.equal(isTrustedSender(top, false), false, "another window");
  assert.equal(isTrustedSender(null, true), false, "frame already gone");
  assert.equal(isTrustedSender({ url: "app://panel/index.html", parent: {} }, true), false, "subframe");
  assert.equal(isTrustedSender({ url: "https://evil.example/", parent: null }, true), false, "foreign origin");
  assert.equal(isTrustedSender({ url: "about:blank", parent: null }, true), false);
});

test("external links are fixed https URLs", () => {
  for (const url of Object.values(EXTERNAL_LINKS)) assert.match(url, /^https:\/\/[a-z0-9.-]+\//);
});
