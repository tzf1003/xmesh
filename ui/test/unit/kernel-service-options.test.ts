import assert from "node:assert/strict";
import * as path from "node:path";
import { test } from "node:test";
import { kernelServiceOptions } from "../../src/main/cli";

test("service options retain the original OAuth password source and local-only mode", async () => {
  const paths = { configFile: path.resolve("custom.yaml"), stateDir: path.resolve("state"), serviceNode: "old-name" };
  const password = path.resolve("custom secret/password");
  const options = await kernelServiceOptions(paths, async (args) => {
    assert.deepEqual(args, ["service-info", "--name", "xmesh-old-name", "--json"]);
    return { code: 0, stdout: JSON.stringify({ messages: [JSON.stringify({ config_file: paths.configFile, state_dir: paths.stateDir,
      oauth_password_file: password, local_only: true })] }), stderr: "" };
  });
  assert.deepEqual(options, ["--local-only", "--oauth-password-file", password]);
});

test("only missing registrations can skip option capture; malformed metadata blocks replacement", async () => {
  const paths = { configFile: path.resolve("config.yaml"), stateDir: path.resolve("state") };
  assert.deepEqual(await kernelServiceOptions(paths, async () => ({ code: 1, stdout: '{"error":{"reason":"not_installed"}}', stderr: "" })), []);
  for (const stdout of ["{}", '{"error":{"reason":"denied"}}']) {
    await assert.rejects(kernelServiceOptions(paths, async () => ({ code: 1, stdout, stderr: "denied" })));
  }
  await assert.rejects(kernelServiceOptions(paths, async () => ({ code: 0, stdout: '{"messages":["{}"]}', stderr: "" })));
});
