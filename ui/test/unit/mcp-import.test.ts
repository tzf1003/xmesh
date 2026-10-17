import assert from "node:assert/strict";
import { test } from "node:test";
import { IMPORT_MAX_BYTES, parseMcpImport } from "../../src/renderer/lib/mcp-import";

const one = (text: string) => parseMcpImport(text);

test("the mcpServers wrapper and the flat name map both import", () => {
  const wrapped = one(JSON.stringify({ mcpServers: { fetch: { command: "uvx", args: ["mcp-server-fetch"] } } }));
  const flat = one(JSON.stringify({ fetch: { command: "uvx", args: ["mcp-server-fetch"] } }));
  assert.deepEqual(wrapped, flat);
  assert.deepEqual(wrapped.entries, [{ name: "fetch", value: { transport: "stdio", command: ["uvx", "mcp-server-fetch"], policy: "shared" }, warnings: [] }]);
  assert.deepEqual(wrapped.problems, []);
});

test("env is dropped with a warning that names the variables, never their values", () => {
  const secret = "sk-very-secret-value-123";
  const r = one(JSON.stringify({ github: { command: "npx", args: ["srv"], env: { GITHUB_TOKEN: secret, DEBUG: "1" } } }));
  assert.equal(r.entries.length, 1);
  assert.deepEqual(r.entries[0]!.value, { transport: "stdio", command: ["npx", "srv"], policy: "shared" });
  assert.deepEqual(r.entries[0]!.warnings, [{ name: "github", code: "env", detail: "GITHUB_TOKEN, DEBUG" }]);
  assert.ok(!JSON.stringify(r).includes(secret), "the value must not reach the preview");
});

test("type and url choose the transport: sse and streamable-http map to http", () => {
  const r = one(
    JSON.stringify({
      a: { type: "sse", url: "http://localhost:9000/sse" },
      b: { type: "streamable-http", url: "http://127.0.0.1:9001/mcp" },
      c: { type: "http", url: "https://[::1]:9002/mcp" },
      d: { url: "http://localhost:9003/mcp" },
    }),
  );
  assert.deepEqual(
    r.entries.map((e) => [e.name, e.value]),
    [
      ["a", { transport: "http", url: "http://localhost:9000/sse", policy: "shared" }],
      ["b", { transport: "http", url: "http://127.0.0.1:9001/mcp", policy: "shared" }],
      ["c", { transport: "http", url: "https://[::1]:9002/mcp", policy: "shared" }],
      ["d", { transport: "http", url: "http://localhost:9003/mcp", policy: "shared" }],
    ],
  );
});

test("a kernel-format entry imports as it is written, with its optional fields", () => {
  const r = one(JSON.stringify({ docs: { transport: "stdio", command: ["npx", "-y", "srv"], policy: "per-caller", idle_timeout: "5m", favorite: true, cwd: " /srv " } }));
  assert.deepEqual(r.entries[0]!.value, {
    transport: "stdio",
    command: ["npx", "-y", "srv"],
    policy: "per-caller",
    cwd: "/srv",
    idle_timeout: "5m",
    favorite: true,
  });
  assert.deepEqual(r.problems, []);
});

test("a public URL is refused for the entry, and the other entries still import", () => {
  const r = one(JSON.stringify({ ok: { command: "npx" }, remote: { url: "https://example.com/mcp" } }));
  assert.deepEqual(
    r.entries.map((e) => e.name),
    ["ok"],
  );
  assert.deepEqual(r.problems, [{ name: "remote", code: "url.host" }]);
});

test("entries with a bad name or a non-object value are reported per entry", () => {
  const r = one(JSON.stringify({ my_server: { command: "npx" }, x: "npx", fine: { command: "a" } }));
  assert.deepEqual(r.problems, [
    { name: "my_server", code: "name.invalid" },
    { name: "x", code: "entry" },
  ]);
  assert.deepEqual(
    r.entries.map((e) => e.name),
    ["fine"],
  );
});

test("the transport and the stdio command are checked", () => {
  const codes = (v: unknown) => one(JSON.stringify({ s: v })).problems.map((p) => [p.code, p.detail]);
  assert.deepEqual(codes({ type: "websocket", url: "http://localhost:1/" }), [["transport", "websocket"]]);
  assert.deepEqual(codes({}), [["command.required", undefined]]);
  assert.deepEqual(codes({ command: "" }), [["command.required", undefined]]);
  assert.deepEqual(codes({ command: ["npx", 3] }), [["command.required", undefined]]);
  assert.deepEqual(codes({ command: [""] }), [["command.required", undefined]]);
  assert.deepEqual(codes({ command: "npx", args: "-y" }), [["args.invalid", undefined]]);
  assert.deepEqual(codes({ type: "http" }), [["url.required", undefined]]);
  assert.deepEqual(codes({ url: 7 }), [["url.invalid", undefined]]);
});

test("policy, idle timeout and cwd are checked when given", () => {
  const codes = (v: unknown) => one(JSON.stringify({ s: v })).problems.map((p) => p.code);
  assert.deepEqual(codes({ command: "npx", policy: "weird" }), ["policy.invalid"]);
  assert.deepEqual(codes({ command: "npx", idle_timeout: "soon" }), ["idle.invalid"]);
  assert.deepEqual(codes({ command: "npx", cwd: 3 }), ["cwd.invalid"]);
  assert.deepEqual(one(JSON.stringify({ s: { command: "npx", idle_timeout: "" } })).problems, [], "an empty idle timeout is absent");
});

test("fields the panel does not manage are listed as ignored, and the parts that do not apply to the transport too", () => {
  const r = one(JSON.stringify({ x: { type: "stdio", command: "npx", url: "http://localhost:1/", headers: { A: "b" }, disabled: true } }));
  assert.deepEqual(r.entries[0]!.warnings, [{ name: "x", code: "ignored", detail: "url, headers, disabled" }]);
  // Without a type, a url makes the entry http, and then the command is the field that does not apply.
  const u = one(JSON.stringify({ z: { command: "npx", url: "http://localhost:1/" } }));
  assert.deepEqual(u.entries[0]!.warnings, [{ name: "z", code: "ignored", detail: "command" }]);
  const h = one(JSON.stringify({ y: { url: "http://localhost:2/", command: "npx" } }));
  assert.deepEqual(h.entries[0]!.warnings, [{ name: "y", code: "ignored", detail: "command" }]);
});

test("text that is not an import is reported as a whole", () => {
  assert.deepEqual(one("not json").problems, [{ name: "", code: "json" }]);
  assert.deepEqual(one("[]").problems, [{ name: "", code: "shape" }]);
  assert.deepEqual(one("{}").problems, [{ name: "", code: "empty" }]);
  assert.deepEqual(one(JSON.stringify({ mcpServers: {} })).problems, [{ name: "", code: "empty" }]);
  // A single server pasted without its name.
  assert.deepEqual(one(JSON.stringify({ command: "npx", args: ["x"] })).problems, [{ name: "", code: "unnamed" }]);
  // A backend that happens to be named "url" is still a name.
  assert.deepEqual(one(JSON.stringify({ url: { command: "npx" } })).entries.map((e) => e.name), ["url"]);
  assert.deepEqual(one("x".repeat(IMPORT_MAX_BYTES + 1)).problems, [{ name: "", code: "tooBig" }]);
});
