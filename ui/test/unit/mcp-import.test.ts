import assert from "node:assert/strict";
import { test } from "node:test";
import { IMPORT_MAX_BYTES, parseMcpImport } from "../../src/renderer/lib/mcp-import";

const one = (text: string) => parseMcpImport(text);

test("UI MCP lifecycle settings survive import and reject incompatible transports",()=>{
  const value={transport:"stdio",command:["node","ui-mcp"],policy:"per-caller",shutdown_timeout:"10s",process_cleanup:"tree"};
  const imported=one(JSON.stringify({ui:value}));assert.deepEqual(imported.entries[0]?.value,value);assert.deepEqual(imported.entries[0]?.warnings,[]);
  for(const change of [{shutdown_timeout:"0s"},{shutdown_timeout:"31s"},{process_cleanup:"all"},{transport:"http",url:"http://localhost:9000/mcp"}])assert.equal(one(JSON.stringify({ui:{...value,...change}})).problems.length,1);
});

test("the mcpServers wrapper and the flat name map both import", () => {
  const wrapped = one(JSON.stringify({ mcpServers: { fetch: { command: "uvx", args: ["mcp-server-fetch"] } } }));
  const flat = one(JSON.stringify({ fetch: { command: "uvx", args: ["mcp-server-fetch"] } }));
  assert.deepEqual(wrapped, flat);
  assert.deepEqual(wrapped.entries, [{ name: "fetch", value: { transport: "stdio", command: ["uvx", "mcp-server-fetch"], policy: "shared" }, warnings: [] }]);
  assert.deepEqual(wrapped.problems, []);
});

test("an stdio entry's env comes out beside the config value, which never holds it", () => {
  const r = one(JSON.stringify({ github: { command: "npx", args: ["srv"], env: { GITHUB_TOKEN: "sk-secret-1", DEBUG: "1" } } }));
  assert.equal(r.entries.length, 1);
  const e = r.entries[0]!;
  assert.deepEqual(e.value, { transport: "stdio", command: ["npx", "srv"], policy: "shared" });
  assert.deepEqual(e.env, { GITHUB_TOKEN: "sk-secret-1", DEBUG: "1" });
  assert.deepEqual(e.warnings, []);
  assert.ok(!JSON.stringify(e.value).includes("sk-secret-1"), "the value must not be part of what is written to the config");
});

test("an empty env block imports as no env", () => {
  const r = one(JSON.stringify({ a: { command: "npx", env: {} } }));
  assert.equal(r.entries[0]!.env, undefined);
  assert.deepEqual(r.entries[0]!.warnings, []);
});

test("an http entry's env is ignored with a warning that names the variables, never their values", () => {
  const secret = "sk-very-secret-value-123";
  const r = one(JSON.stringify({ remote: { url: "http://localhost:9001/mcp", env: { API_KEY: secret, DEBUG: "1" } } }));
  assert.equal(r.entries.length, 1);
  assert.equal(r.entries[0]!.env, undefined);
  assert.deepEqual(r.entries[0]!.warnings, [{ name: "remote", code: "envIgnored", detail: "API_KEY, DEBUG" }]);
  assert.ok(!JSON.stringify(r).includes(secret), "the value must not reach the preview");
});

test("an env block that cannot be used refuses the entry and names only the bad variables", () => {
  const secret = "sk-bad-name-secret";
  const bad = one(JSON.stringify({ a: { command: "npx", env: { "BAD NAME": secret, GOOD: "1" } }, b: { command: "npx" } }));
  assert.deepEqual(bad.entries.map((e) => e.name), ["b"]);
  assert.deepEqual(bad.problems, [{ name: "a", code: "env.invalid", detail: "BAD NAME" }]);
  assert.ok(!JSON.stringify(bad).includes(secret));

  const notString = one(JSON.stringify({ a: { command: "npx", env: { PORT: 8080 } } }));
  assert.deepEqual(notString.problems, [{ name: "a", code: "env.invalid", detail: "PORT" }]);

  const nul = one(JSON.stringify({ a: { command: "npx", env: { K: "a\u0000b" } } }));
  assert.deepEqual(nul.problems, [{ name: "a", code: "env.invalid", detail: "K" }]);

  const shape = one(JSON.stringify({ a: { command: "npx", env: ["X=1"] } }));
  assert.deepEqual(shape.problems, [{ name: "a", code: "env.shape" }]);

  const many = Object.fromEntries(Array.from({ length: 65 }, (_, i) => [`V${i}`, "1"]));
  assert.deepEqual(one(JSON.stringify({ a: { command: "npx", env: many } })).problems, [{ name: "a", code: "env.tooMany" }]);

  const big = one(JSON.stringify({ a: { command: "npx", env: { K: "x".repeat(8193) } } }));
  assert.deepEqual(big.problems, [{ name: "a", code: "env.invalid", detail: "K" }]);
});

test("type and url choose the transport: streamable-http and http map to http", () => {
  const r = one(
    JSON.stringify({
      b: { type: "streamable-http", url: "http://127.0.0.1:9001/mcp" },
      c: { type: "http", url: "https://[::1]:9002/mcp" },
      d: { url: "http://localhost:9003/mcp" },
    }),
  );
  assert.deepEqual(
    r.entries.map((e) => [e.name, e.value]),
    [
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

test("an sse entry is refused, since the kernel cannot connect to it", () => {
  const r = one(JSON.stringify({ legacy: { type: "sse", url: "http://100.64.0.1:8080/sse" } }));
  assert.deepEqual(r.entries, []);
  assert.deepEqual(r.problems, [{ name: "legacy", code: "transport", detail: "sse" }]);
});
