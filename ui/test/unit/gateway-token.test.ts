import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, test } from "node:test";
import { GatewayCredentials, TokenUnavailableError, firstPatByName, readTokenFile } from "../../src/main/gateway-token";
import type { ShellController } from "../../src/main/controller";
import type { PatInfo } from "../../src/shared/control";

let dir: string;
beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-tok-"));
});
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

const TOKEN = "tok-0123456789_abcdEF";

function patAt(name: string, file: string): PatInfo {
  return { name, scopes: ["devices:read"], token_file: file, modified_at: "2026-10-16T15:30:00.123Z" };
}

/** A controller stand-in: `requireCapable` hands back a client whose `call` answers GET /v1/pats with `pats`. */
function controllerWith(pats: unknown, capable = true): Pick<ShellController, "requireCapable"> {
  const client = { call: async () => pats };
  return {
    requireCapable: () => {
      if (!capable) throw new Error("this kernel does not support pats");
      return client as unknown as ReturnType<ShellController["requireCapable"]>;
    },
  };
}

test("the first PAT by name is the one whose token the commands carry", () => {
  const list = [patAt("default", "/x/default.pat"), patAt("claude-code", "/x/cc.pat"), patAt("Zed", "/x/z.pat")];
  assert.equal(firstPatByName(list)?.name, "Zed", "plain code-unit order: uppercase sorts before lowercase");
  assert.equal(firstPatByName([patAt("ci", "/a"), patAt("claude-code", "/b")])?.name, "ci");
  assert.equal(firstPatByName([]), undefined);
});

test("a token file yields its trimmed token", () => {
  const file = path.join(dir, "claude-code.pat");
  fs.writeFileSync(file, `${TOKEN}\n`);
  assert.equal(readTokenFile(file), TOKEN);
  fs.writeFileSync(file, `  ${TOKEN} \r\n`);
  assert.equal(readTokenFile(file), TOKEN);
});

test("a missing, empty, oversized, relative or non-regular token file is refused", () => {
  assert.throws(() => readTokenFile(path.join(dir, "missing.pat")), TokenUnavailableError);

  const empty = path.join(dir, "empty.pat");
  fs.writeFileSync(empty, "\n  \n");
  assert.throws(() => readTokenFile(empty), /empty/);

  const big = path.join(dir, "big.pat");
  fs.writeFileSync(big, "x".repeat(5000));
  assert.throws(() => readTokenFile(big), /too large/);

  assert.throws(() => readTokenFile("relative.pat"), /not absolute/);

  assert.throws(() => readTokenFile(dir), /not a regular file/, "a directory is not a token");

  const real = path.join(dir, "real.pat");
  fs.writeFileSync(real, TOKEN);
  const link = path.join(dir, "link.pat");
  fs.symlinkSync(real, link);
  assert.throws(() => readTokenFile(link), /not a regular file/, "a symlink is not read through");
});

test("a token file holding characters a connect command cannot carry is refused", () => {
  const file = path.join(dir, "bad.pat");
  fs.writeFileSync(file, 'tok"en-with-quotes\n');
  assert.throws(() => readTokenFile(file), /cannot carry/);
});

test("the credential reader takes the first PAT's token file from the kernel", async () => {
  const file = path.join(dir, "claude-code.pat");
  fs.writeFileSync(file, `${TOKEN}\n`);
  const creds = new GatewayCredentials(controllerWith({ enforced: true, pats: [patAt("default", path.join(dir, "default.pat")), patAt("claude-code", file)] }));
  assert.equal(await creds.read(), TOKEN);
});

test("no PAT, no capability, or an unreadable file: the reader throws, never returns a placeholder", async () => {
  await assert.rejects(new GatewayCredentials(controllerWith({ enforced: false, pats: [] })).read(), TokenUnavailableError);
  await assert.rejects(new GatewayCredentials(controllerWith(undefined, false)).read(), TokenUnavailableError);
  await assert.rejects(new GatewayCredentials(controllerWith({ enforced: true, pats: [patAt("default", path.join(dir, "nope.pat"))] })).read(), TokenUnavailableError);
});
