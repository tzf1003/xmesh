import assert from "node:assert/strict";
import { test } from "node:test";
import { cliArgs } from "../../src/main/cli";

test("restart and stop name the node's service, so no config file in the working directory is needed", () => {
  assert.deepEqual(cliArgs("restart", "office-pc"), ["restart", "--name", "xmesh-office-pc", "--json"]);
  assert.deepEqual(cliArgs("stop", "office-pc"), ["stop", "--name", "xmesh-office-pc", "--json"]);
});

test("without a known node the command runs as before", () => {
  assert.deepEqual(cliArgs("restart"), ["restart", "--json"]);
  assert.deepEqual(cliArgs("restart", ""), ["restart", "--json"]);
});

test("a directory name that is not a plain node name is not passed on", () => {
  assert.deepEqual(cliArgs("stop", "../x"), ["stop", "--json"]);
  assert.deepEqual(cliArgs("stop", "a b"), ["stop", "--json"]);
});
