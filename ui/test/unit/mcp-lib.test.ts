import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import { BUILTIN_TOOLS, BUILTIN_TOOL_NAMES, isBuiltinTool } from "../../src/shared/builtin-tools";
import { en, zh } from "../../src/renderer/messages";
import { authzRows } from "../../src/renderer/lib/config";
import {
  BACKEND_NAME,
  DURATION,
  TAG_KEY,
  type BackendForm,
  type AuthzForm,
  authzDeleteEdits,
  authzSaveEdits,
  authzState,
  argvLines,
  backendDeleteEdits,
  backendEdits,
  backendRows,
  checkAuthzForm,
  checkBackendForm,
  checkBackendUrl,
  disabledTools,
  exactGrants,
  grantEdits,
  grantedTags,
  matchesPattern,
  orderTools,
  pointer,
  toolToggleEdits,
} from "../../src/renderer/lib/mcp";
import { schemaDir } from "../../testserver/fixtures";

const parsedWith = (agent: unknown): unknown => ({ node: { name: "n1", roles: ["agent"] }, agent });

const formBase: BackendForm = {
  name: "git-tools",
  transport: "stdio",
  command: "git\nmcp-server",
  url: "",
  cwd: "",
  policy: "shared",
  idleTimeout: "",
  grants: [],
  newTag: "",
  favorite: false,
};

test("the built-in tool catalog is the enum of agent.disabled_tools in the config schema", () => {
  // Fails until the kernel owner adds disabled_tools (with its enum) to docs/contracts/schemas/config.schema.json.
  const schema = JSON.parse(fs.readFileSync(path.join(schemaDir(), "config.schema.json"), "utf8")) as {
    properties: { agent: { properties: { disabled_tools?: { items?: { enum?: string[] } } } } };
  };
  const names = schema.properties.agent.properties.disabled_tools?.items?.enum;
  assert.ok(Array.isArray(names), "agent.disabled_tools items must be an enum");
  assert.deepEqual([...BUILTIN_TOOL_NAMES].sort(), [...names].sort());
});

test("the catalog has unique names, both descriptions, and no empty text", () => {
  assert.equal(new Set(BUILTIN_TOOL_NAMES).size, BUILTIN_TOOLS.length);
  for (const t of BUILTIN_TOOLS) {
    assert.ok(t.zh.length > 0 && t.en.length > 0, t.name);
    assert.doesNotMatch(t.en, /[一-鿿]/, t.name);
  }
  assert.ok(isBuiltinTool("exec.run"));
  assert.ok(!isBuiltinTool("exec"));
});

test("backend names follow the schema's backendName grammar", () => {
  for (const ok of ["a", "git-tools", "A1", "x".repeat(63)]) assert.ok(BACKEND_NAME.test(ok), ok);
  for (const bad of ["", "-a", "a_b", "a b", "x".repeat(64), "中文", "a/b", "a.b"]) assert.ok(!BACKEND_NAME.test(bad), bad);
});

test("tag keys follow the schema's agent.authz grammar", () => {
  for (const ok of ["tag:ops", "tag:a_b*", "tag:*", "tag:dev-1"]) assert.ok(TAG_KEY.test(ok), ok);
  for (const bad of ["ops", "tag:", "tag:a b", "tag:a/b", "Tag:ops", "tag:a.b"]) assert.ok(!TAG_KEY.test(bad), bad);
});

test("idle timeouts follow the schema's duration grammar", () => {
  for (const ok of ["5m", "30s", "1h30m", "1.5h", "250ms", "2µs", "10ns"]) assert.ok(DURATION.test(ok), ok);
  for (const bad of ["5", "m5", "5 m", "5min", "", "1h 30m", "-5m"]) assert.ok(!DURATION.test(bad), bad);
});

test("http URLs are accepted only where the kernel accepts them", () => {
  for (const ok of [
    "http://localhost:7433/mcp",
    "http://127.0.0.1:8080",
    "https://127.5.6.7/x",
    "http://[::1]:9000/x",
    "http://100.64.0.1:80",
    "http://100.127.255.254",
    "http://[fd7a:115c:a1e0::1]:8080/mcp",
    "http://[fd7a:115c:a1e0:ffff::1]/",
  ]) {
    assert.equal(checkBackendUrl(ok), undefined, ok);
  }
  const expected: [string, string][] = [
    ["http://example.com", "url.host"],
    ["https://8.8.8.8", "url.host"],
    ["http://100.128.0.1", "url.host"],
    ["http://100.63.255.255", "url.host"],
    ["http://[fd7a:115c:a1e1::1]", "url.host"],
    ["http://[::2]", "url.host"],
    ["http://LOCALHOST", "url.host"],
    ["http://127.1:8080", "url.host"],
    ["ftp://localhost", "url.scheme"],
    ["localhost:8080", "url.scheme"],
    ["http://user:pw@localhost", "url.userinfo"],
    ["http://@localhost", "url.userinfo"],
  ];
  for (const [url, code] of expected) assert.equal(checkBackendUrl(url), code, url);
});

test("argv lines are trimmed and blank lines are dropped", () => {
  assert.deepEqual(argvLines("  npx \n\n -y\r\n  pkg with space \n"), ["npx", "-y", "pkg with space"]);
  assert.deepEqual(argvLines(" \n \n"), []);
});

test("a stdio form becomes a stdio value with argv, cwd and idle timeout trimmed", () => {
  const r = checkBackendForm({ ...formBase, command: "npx\n\n-y\n srv ", cwd: " /srv/docs ", idleTimeout: "5m", policy: "exclusive", grants: ["tag:ops"], newTag: " tag:dev " }, ["docs"]);
  assert.ok(r.ok);
  assert.equal(r.name, "git-tools");
  assert.deepEqual(r.value, { transport: "stdio", command: ["npx", "-y", "srv"], policy: "exclusive", cwd: "/srv/docs", idle_timeout: "5m" });
  assert.deepEqual(r.tags, ["tag:ops", "tag:dev"]);
});

test("an http form stores the URL and never a command", () => {
  const r = checkBackendForm({ ...formBase, transport: "http", command: "ignored", url: " http://localhost:7433/mcp ", favorite: true }, []);
  assert.ok(r.ok);
  assert.deepEqual(r.value, { transport: "http", url: "http://localhost:7433/mcp", policy: "shared", favorite: true });
});

test("form problems come back as codes, one per field", () => {
  const codes = (f: Partial<BackendForm>, existing: string[] = [], editing?: string) => {
    const r = checkBackendForm({ ...formBase, ...f }, existing, editing);
    return r.ok ? [] : r.problems.map((p) => `${p.field}:${p.code}`);
  };
  assert.deepEqual(codes({ name: "" }), ["name:name.invalid"]);
  assert.deepEqual(codes({ name: "docs" }, ["docs"]), ["name:name.duplicate"]);
  assert.deepEqual(codes({ name: "docs" }, ["docs"], "docs"), [], "editing keeps its own name");
  assert.deepEqual(codes({ command: " \n " }), ["command:command.required"]);
  assert.deepEqual(codes({ transport: "http", url: "" }), ["url:url.required"]);
  assert.deepEqual(codes({ transport: "http", url: "https://example.com/mcp" }), ["url:url.host"]);
  assert.deepEqual(codes({ policy: "weird" }), ["policy:policy.invalid"]);
  assert.deepEqual(codes({ idleTimeout: "soon" }), ["idle:idle.invalid"]);
  assert.deepEqual(codes({ newTag: "ops" }), ["tag:tag.invalid"]);
});

test("a backend form keeps favorite from the stored entry", () => {
  const r = checkBackendForm({ ...formBase, favorite: true }, [], "git-tools");
  assert.ok(r.ok);
  assert.equal(r.value.favorite, true);
});

test("JSON pointers escape / and ~ and join segments", () => {
  assert.equal(pointer("agent", "authz", "tag:a/b~c"), "/agent/authz/tag:a~1b~0c");
  assert.equal(pointer("agent", "disabled_tools"), "/agent/disabled_tools");
});

test("pattern matching: * is a wildcard and every other character is literal", () => {
  assert.ok(matchesPattern("fs-*", "fs-docs"));
  assert.ok(matchesPattern("docs", "docs"));
  assert.ok(!matchesPattern("docs", "docs2"));
  assert.ok(matchesPattern("a.b*", "a.bc"));
  assert.ok(!matchesPattern("a.b*", "axbc"), "the dot is not a wildcard");
  assert.ok(matchesPattern("*", "anything"));
});

test("read helpers: backend rows, disabled tools, granted tags by name and by pattern", () => {
  const parsed = parsedWith({
    backends: {
      docs: { transport: "stdio", command: ["npx", "srv"], policy: "shared", idle_timeout: "5m", favorite: true, cwd: "/x" },
      "fs-docs": { transport: "http", url: "http://localhost:9/mcp", policy: "per-caller" },
    },
    disabled_tools: ["exec.run", "zz.future"],
    authz: {
      "tag:ops": { backends: ["docs", "fs-*"], tools: ["fs.read"] },
      "tag:dev": { backends: ["docs"], tools: [] },
      "tag:none": { backends: ["other"], tools: [] },
    },
  });
  assert.deepEqual(backendRows(parsed)[0], {
    name: "docs",
    transport: "stdio",
    command: ["npx", "srv"],
    url: "",
    cwd: "/x",
    policy: "shared",
    idleTimeout: "5m",
    favorite: true,
  });
  assert.deepEqual(disabledTools(parsed), ["exec.run", "zz.future"]);
  const rows = authzRows(parsed);
  assert.deepEqual(grantedTags(rows, "fs-docs"), ["tag:ops"]);
  assert.deepEqual(grantedTags(rows, "docs"), ["tag:ops", "tag:dev"]);
  assert.deepEqual(exactGrants(rows, "fs-docs"), []);
  assert.deepEqual(exactGrants(rows, "docs"), ["tag:ops", "tag:dev"]);
  assert.deepEqual(disabledTools(parsedWith({})), []);
  assert.deepEqual(backendRows(null), []);
});

test("orderTools puts the catalog first and keeps unknown names after it", () => {
  assert.deepEqual(orderTools(["pty.open", "zz.future", "exec.run", "pty.open"]), ["exec.run", "pty.open", "zz.future"]);
});

test("toggling a built-in tool off writes the whole list; toggling the last one back on deletes the key", () => {
  const key = "/agent/disabled_tools";
  assert.deepEqual(toolToggleEdits([], "exec.run", false), [{ path: key, value: ["exec.run"] }]);
  assert.deepEqual(toolToggleEdits(["exec.run"], "exec.run", true), [{ path: key, delete: true }]);
  assert.deepEqual(toolToggleEdits(["pty.open", "exec.run"], "exec.run", true), [{ path: key, value: ["pty.open"] }]);
  assert.deepEqual(toolToggleEdits(["zz.future"], "exec.run", false), [{ path: key, value: ["exec.run", "zz.future"] }]);
  // Already in the requested state: nothing to write.
  assert.deepEqual(toolToggleEdits(["exec.run"], "exec.run", false), []);
  assert.deepEqual(toolToggleEdits([], "exec.run", true), []);
});

test("deleting a backend removes its exact name from every tag list in the same batch", () => {
  const rows = authzRows(
    parsedWith({
      authz: {
        "tag:ops": { backends: ["docs", "fs-*"], tools: [] },
        "tag:dev": { backends: ["git", "docs"], tools: ["fs.read"] },
        "tag:none": { backends: ["git"], tools: [] },
      },
    }),
  );
  assert.deepEqual(backendDeleteEdits(rows, "docs"), [
    { path: "/agent/backends/docs", delete: true },
    { path: "/agent/authz/tag:ops/backends", value: ["fs-*"] },
    { path: "/agent/authz/tag:dev/backends", value: ["git"] },
  ]);
  // A tag left with no backends keeps an empty list: the schema requires both keys.
  assert.deepEqual(backendDeleteEdits(rows, "git").slice(1), [
    { path: "/agent/authz/tag:dev/backends", value: ["docs"] },
    { path: "/agent/authz/tag:none/backends", value: [] },
  ]);
});

test("a new backend granted to a tag that does not exist creates the tag in the same batch", () => {
  const state = authzState([]);
  const edits = backendEdits(state, "git-tools", { transport: "stdio", command: ["git"], policy: "shared" }, ["tag:ops"], true);
  assert.deepEqual(edits, [
    { path: "/agent/backends/git-tools", value: { transport: "stdio", command: ["git"], policy: "shared" } },
    { path: "/agent/authz/tag:ops", value: { backends: ["git-tools"], tools: [] } },
  ]);
});

test("several backends granted the same tag in one batch compose, each list built on the one before", () => {
  const state = authzState([]);
  const a = backendEdits(state, "a", { transport: "stdio", command: ["a"], policy: "shared" }, ["tag:ops"], true);
  const b = backendEdits(state, "b", { transport: "stdio", command: ["b"], policy: "shared" }, ["tag:ops"], true);
  assert.deepEqual(b.at(-1), { path: "/agent/authz/tag:ops/backends", value: ["a", "b"] });
  assert.equal(a.length, 2);
});

test("granting with prune removes the name from tags no longer wanted and adds the new ones", () => {
  const rows = authzRows(
    parsedWith({
      authz: {
        "tag:ops": { backends: ["git"], tools: [] },
        "tag:dev": { backends: ["git", "x"], tools: [] },
      },
    }),
  );
  const edits = backendEdits(authzState(rows), "git", { transport: "stdio", command: ["git"], policy: "shared" }, ["tag:dev", "tag:new"], true);
  assert.deepEqual(edits.slice(1), [
    { path: "/agent/authz/tag:ops/backends", value: [] },
    { path: "/agent/authz/tag:new", value: { backends: ["git"], tools: [] } },
  ]);
});

test("granting without prune only adds: existing grants stay", () => {
  const rows = authzRows(parsedWith({ authz: { "tag:ops": { backends: ["git"], tools: [] } } }));
  assert.deepEqual(grantEdits(authzState(rows), "git", [], false), []);
  assert.deepEqual(grantEdits(authzState(rows), "git", ["tag:dev"], false), [{ path: "/agent/authz/tag:dev", value: { backends: ["git"], tools: [] } }]);
});

test("tag form: tools in catalog order, extra patterns appended once, backends checked first", () => {
  const form: AuthzForm = {
    tag: "tag:ops",
    builtin: ["fs.write", "fs.read"],
    extraTools: "fs.*\n  \nfs.*",
    backends: ["docs"],
    extraBackends: "fs-*",
  };
  const r = checkAuthzForm(form, []);
  assert.ok(r.ok);
  assert.deepEqual(r.tools, ["fs.read", "fs.write", "fs.*"]);
  assert.deepEqual(r.backends, ["docs", "fs-*"]);
});

test("tag form problems: the key rule, duplicates (not against itself), and spaces in patterns", () => {
  const base: AuthzForm = { tag: "tag:ops", builtin: [], extraTools: "", backends: [], extraBackends: "" };
  const codes = (f: Partial<AuthzForm>, existing: string[]) => {
    const r = checkAuthzForm({ ...base, ...f }, existing);
    return r.ok ? [] : r.problems.map((p) => `${p.field}:${p.code}`);
  };
  assert.deepEqual(codes({ tag: "ops" }, []), ["tag:tag.invalid"]);
  assert.deepEqual(codes({}, ["tag:ops"]), ["tag:tag.duplicate"]);
  assert.deepEqual(codes({ original: "tag:ops" }, ["tag:ops"]), [], "keeping the same key is not a duplicate");
  assert.deepEqual(codes({ extraTools: "fs read" }, []), ["extraTools:extra.space"]);
  assert.deepEqual(codes({ extraBackends: "a b" }, []), ["extraBackends:extra.space"]);
});

test("saving a tag writes its whole value; renaming also removes the old key; deleting removes the key", () => {
  assert.deepEqual(authzSaveEdits(undefined, "tag:ops", ["fs.read"], []), [{ path: "/agent/authz/tag:ops", value: { backends: [], tools: ["fs.read"] } }]);
  assert.deepEqual(authzSaveEdits("tag:old", "tag:new", [], ["git"]), [
    { path: "/agent/authz/tag:old", delete: true },
    { path: "/agent/authz/tag:new", value: { backends: ["git"], tools: [] } },
  ]);
  assert.deepEqual(authzDeleteEdits("tag:ops"), [{ path: "/agent/authz/tag:ops", delete: true }]);
});

test("every problem and warning code the lib or importer can produce has a message in both tables", () => {
  const codes = [
    "name.invalid", "name.duplicate", "command.required", "url.required", "url.invalid", "url.scheme", "url.userinfo", "url.host",
    "policy.invalid", "idle.invalid", "tag.invalid", "tag.duplicate", "extra.space",
    "json", "shape", "unnamed", "empty", "tooBig", "entry", "transport", "args.invalid", "cwd.invalid", "tooMany",
  ];
  for (const c of codes) {
    assert.ok(`mcp.err.${c}` in zh, `zh has mcp.err.${c}`);
    assert.ok(`mcp.err.${c}` in en, `en has mcp.err.${c}`);
  }
  for (const c of ["env", "ignored"]) {
    assert.ok(`mcp.warn.${c}` in zh && `mcp.warn.${c}` in en, c);
  }
});
