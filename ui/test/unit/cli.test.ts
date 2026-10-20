import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { cliArgs, cliCommandText, quoteForTerminal, xmeshBinary } from "../../src/main/cli";

test("start, restart and stop name the node's service, so no config file in the working directory is needed", () => {
  assert.deepEqual(cliArgs("start", "office-pc"), ["start", "--name", "xmesh-office-pc", "--json"]);
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

test("a terminal runs the binary as written: plain when safe, quoted otherwise, with & in PowerShell", () => {
  assert.equal(quoteForTerminal("/opt/x/xmesh", "linux"), "/opt/x/xmesh");
  assert.equal(quoteForTerminal("/Users/me/My Tools/xmesh", "darwin"), "'/Users/me/My Tools/xmesh'");
  assert.equal(quoteForTerminal("/Users/me/it's/xmesh", "darwin"), "'/Users/me/it'\\''s/xmesh'");
  assert.equal(quoteForTerminal("C:\\Tools\\it's\\xmesh.exe", "win32"), "& 'C:\\Tools\\it''s\\xmesh.exe'");
});

test("the command text names the kernel the app runs, or the bare name when there is none", () => {
  assert.equal(cliCommandText({}, undefined, "darwin"), quoteForTerminal(xmeshBinary({},undefined,"darwin"),"darwin"));
  assert.equal(cliCommandText({ XMESH_BIN: "/opt/x/xmesh" }, undefined, "linux"), "/opt/x/xmesh");
  assert.equal(cliCommandText({ XMESH_BIN: "/Users/me/My Tools/xmesh" }, undefined, "darwin"), "'/Users/me/My Tools/xmesh'");
  assert.equal(cliCommandText({ XMESH_BIN: "C:\\Tools\\it's\\xmesh.exe" }, undefined, "win32"), "& 'C:\\Tools\\it''s\\xmesh.exe'");
});

test("a bundled kernel is named by its path inside the app's resources", () => {
  const res = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-res-"));
  try {
    fs.mkdirSync(path.join(res, "bin"));
    const bundled = path.join(res, "bin", "xmesh");
    fs.writeFileSync(bundled, "");
    assert.equal(cliCommandText({}, res, "linux"), quoteForTerminal(bundled, "linux"));
    assert.equal(cliCommandText({}, path.join(res, "missing"), "linux"), quoteForTerminal(xmeshBinary({},path.join(res,"missing"),"linux"),"linux"));
  } finally {
    fs.rmSync(res, { recursive: true, force: true });
  }
});

test("copied CLI commands use a kernel discovered on PATH",()=>{
  const root=fs.mkdtempSync(path.join(os.tmpdir(),"xmesh-copy-cli-"));
  try {
    const exe=path.join(root,process.platform==="win32"?"xmesh.exe":"xmesh");
    fs.writeFileSync(exe,"");
    const env={PATH:root,Path:root};
    assert.equal(cliCommandText(env,undefined,process.platform),quoteForTerminal(exe));
  } finally {fs.rmSync(root,{recursive:true,force:true});}
});
