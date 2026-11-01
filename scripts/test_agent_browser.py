#!/usr/bin/env python3
"""Unit tests for packages/agent-browser/bin/xmesh-agent-browser.mjs using a stub binary (stdlib only).

The real browser path is covered by accept_browser.py; these tests pin the launcher's own
behaviour: required allowlist, safe defaults, refusal to attach to a user browser, one
namespace per launch, and cleanup after stdin closes.
"""

import json
import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
LAUNCHER = HERE.parent / "packages" / "agent-browser" / "bin" / "xmesh-agent-browser.mjs"

STUB = """#!/usr/bin/env node
import fs from "node:fs";
const [command, ...rest] = process.argv.slice(2);
const record = { command, rest, namespace: process.env.AGENT_BROWSER_NAMESPACE, env: process.env };
fs.appendFileSync(process.env.STUB_LOG, JSON.stringify(record) + "\\n");
if (command === "mcp") {
  const dir = process.env.AGENT_BROWSER_ACTION_POLICY.replace(/policy\\.json$/, "");
  fs.appendFileSync(process.env.STUB_LOG, JSON.stringify({ policy: JSON.parse(fs.readFileSync(process.env.AGENT_BROWSER_ACTION_POLICY, "utf8")), dir }) + "\\n");
  process.stdin.resume();
  process.stdin.on("end", () => process.exit(0));
}
"""


@unittest.skipUnless(shutil.which("node") and sys.platform != "win32", "needs node on a Unix host")
class Launcher(unittest.TestCase):
    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.stub = self.tmp / "agent-browser"
        self.stub.write_text(STUB)
        self.stub.chmod(self.stub.stat().st_mode | stat.S_IXUSR)
        self.log = self.tmp / "log.jsonl"
        (self.tmp / "tmp").mkdir()

    def run_launcher(self, *args, env=None, stdin=b""):
        base = {k: v for k, v in os.environ.items() if not k.startswith(("AGENT_BROWSER_", "BROWSER_BACKEND_"))}
        base.update(BROWSER_BACKEND_BIN=str(self.stub), STUB_LOG=str(self.log), BROWSER_BACKEND_TMP=str(self.tmp / "tmp"),
                    HOME=str(self.tmp))
        base.update(env or {})
        return subprocess.run(["node", str(LAUNCHER), *args], input=stdin, env=base, capture_output=True, timeout=30)

    def records(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_requires_domain_allowlist(self):
        done = self.run_launcher()
        self.assertEqual(done.returncode, 2)
        self.assertIn(b"AGENT_BROWSER_ALLOWED_DOMAINS", done.stderr)
        self.assertEqual(self.records(), [])

    def test_star_needs_explicit_opt_in(self):
        self.assertEqual(self.run_launcher(env={"AGENT_BROWSER_ALLOWED_DOMAINS": "*"}).returncode, 2)
        done = self.run_launcher(env={"AGENT_BROWSER_ALLOWED_DOMAINS": "*", "BROWSER_BACKEND_UNRESTRICTED_DOMAINS": "1"})
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertNotIn("AGENT_BROWSER_ALLOWED_DOMAINS", self.records()[0]["env"])

    def test_defaults_and_policy(self):
        done = self.run_launcher(env={"AGENT_BROWSER_ALLOWED_DOMAINS": "example.com"})
        self.assertEqual(done.returncode, 0, done.stderr)
        start = self.records()[0]
        self.assertEqual(start["rest"], ["--tools", "core,tabs"])
        env = start["env"]
        self.assertEqual(env["AGENT_BROWSER_CONTENT_BOUNDARIES"], "1")
        self.assertEqual(env["AGENT_BROWSER_MAX_OUTPUT"], "50000")
        self.assertEqual(env["AGENT_BROWSER_IDLE_TIMEOUT_MS"], "300000")
        self.assertEqual(env["AGENT_BROWSER_ALLOWED_DOMAINS"], "example.com")
        policy = self.records()[1]["policy"]
        self.assertEqual(policy["default"], "allow")
        self.assertTrue({"evaluate", "download", "upload"} <= set(policy["deny"]))

    def test_node_owner_settings_win(self):
        done = self.run_launcher("--tools", "core,state", env={
            "AGENT_BROWSER_ALLOWED_DOMAINS": "example.com", "AGENT_BROWSER_MAX_OUTPUT": "100",
            "BROWSER_BACKEND_EXTRA_DENY": "cookies"})
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(self.records()[0]["rest"], ["--tools", "core,state"])
        self.assertEqual(self.records()[0]["env"]["AGENT_BROWSER_MAX_OUTPUT"], "100")
        self.assertIn("cookies", self.records()[1]["policy"]["deny"])

    def test_tool_profiles_validated(self):
        env = {"AGENT_BROWSER_ALLOWED_DOMAINS": "example.com"}
        self.assertEqual(self.run_launcher("--tools", "core,bogus", env=env).returncode, 2)
        self.assertEqual(self.run_launcher("--tools", "all", env=env).returncode, 2)
        self.assertEqual(self.run_launcher("--tools", "all", env={**env, "BROWSER_BACKEND_ALLOW_ALL_TOOLS": "1"}).returncode, 0)

    def test_refuses_user_browser(self):
        for name in ("AGENT_BROWSER_CDP", "AGENT_BROWSER_AUTO_CONNECT", "AGENT_BROWSER_PROFILE", "AGENT_BROWSER_STATE"):
            done = self.run_launcher(env={"AGENT_BROWSER_ALLOWED_DOMAINS": "example.com", name: "x"})
            self.assertEqual(done.returncode, 2, name)
        done = self.run_launcher(env={"AGENT_BROWSER_ALLOWED_DOMAINS": "example.com", "AGENT_BROWSER_CDP": "9222",
                                      "BROWSER_BACKEND_ALLOW_USER_BROWSER": "1"})
        self.assertEqual(done.returncode, 0, done.stderr)

    def test_namespace_per_launch_and_cleanup(self):
        env = {"AGENT_BROWSER_ALLOWED_DOMAINS": "example.com"}
        self.run_launcher(env=env)
        self.run_launcher(env=env)
        starts = [r for r in self.records() if r.get("command") == "mcp"]
        closes = [r for r in self.records() if r.get("command") == "close"]
        self.assertEqual(len(starts), 2)
        self.assertNotEqual(starts[0]["namespace"], starts[1]["namespace"])
        self.assertEqual([c["rest"] for c in closes], [["--all"], ["--all"]])
        self.assertEqual([c["namespace"] for c in closes], [s["namespace"] for s in starts])
        self.assertEqual(list((self.tmp / "tmp").iterdir()), [], "policy directory was not removed")


if __name__ == "__main__":
    unittest.main()
