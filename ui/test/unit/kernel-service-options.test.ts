import assert from "node:assert/strict";
import * as path from "node:path";
import { test } from "node:test";
import { kernelServiceInfo, kernelServiceOptions, sameKernelPath } from "../../src/main/cli";

test("service options retain the original OAuth password source and local-only mode", async () => {
  const paths = { configFile: path.resolve("custom.yaml"), stateDir: path.resolve("state"), serviceNode: "old-name" };
  const password = path.resolve("custom secret/password");
  const options = await kernelServiceOptions(paths, async (args) => {
    assert.deepEqual(args, ["service-info", "--name", "xmesh-old-name", "--json"]);
    return { code: 0, stdout: JSON.stringify({ messages: [JSON.stringify({ config_file: paths.configFile, state_dir: paths.stateDir,
      oauth_password_file: password, local_only: true, system: false })] }), stderr: "" };
  });
  assert.deepEqual(options, ["--local-only", "--oauth-password-file", password]);
});

test("service metadata recovers custom configuration paths without a responsive control channel", async () => {
  const config = path.resolve("external/custom.yaml"), state = path.resolve("office_1");
  const reply = { config_file: config, state_dir: state, oauth_password_file: "", local_only: false, system: true };
  const info = await kernelServiceInfo({ configFile: path.resolve("synthetic/xmesh.yaml"), serviceNode: "office" }, async args => {
    assert.ok(args.includes("xmesh-office"));
    return { code: 0, stdout: JSON.stringify({ messages: [JSON.stringify(reply)] }), stderr: "" };
  });
  assert.deepEqual(info, reply);
});

test("only missing registrations can skip option capture; malformed metadata blocks replacement", async () => {
  const paths = { configFile: path.resolve("config.yaml"), stateDir: path.resolve("state") };
  assert.deepEqual(await kernelServiceOptions(paths, async () => ({ code: 1, stdout: '{"error":{"reason":"not_installed"}}', stderr: "" })), []);
  for (const stdout of ["{}", '{"error":{"reason":"denied"}}']) {
    await assert.rejects(kernelServiceOptions(paths, async () => ({ code: 1, stdout, stderr: "denied" })));
  }
  await assert.rejects(kernelServiceOptions(paths, async () => ({ code: 0, stdout: '{"messages":["{}"]}', stderr: "" })));
});

test("Windows registration paths ignore drive and directory casing while POSIX paths retain it", () => {
  assert.equal(sameKernelPath("c:\\Users\\Office\\xmesh.yaml", "C:\\users\\office\\xmesh.yaml", "win32"), true);
  assert.equal(sameKernelPath("C:\\Office\\xmesh.yaml", "C:\\Other\\xmesh.yaml", "win32"), false);
  assert.equal(sameKernelPath("/Users/Office/xmesh.yaml", "/Users/office/xmesh.yaml", "linux"), false);
});
