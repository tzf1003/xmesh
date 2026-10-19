#!/usr/bin/env python3
"""Unit tests for the real-environment acceptance helpers (stdlib only). Run through test_scripts.py."""

import http.server
import json
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import accept_collect  # noqa: E402
import accept_net  # noqa: E402
import accept_pty_budget  # noqa: E402
import accept_windows_l2  # noqa: E402
import smoke_local  # noqa: E402


def generate(*args):
    done = subprocess.run([sys.executable, str(HERE / "accept_config.py"), *args], capture_output=True, text=True)
    return done.returncode, done.stdout, done.stderr


class GatewayTokenDiscovery(unittest.TestCase):
    def test_uses_configured_path_after_interrupted_creation(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = pathlib.Path(tmp)
            (state / "pats").mkdir()
            (state / "pats" / "default.token").write_text("orphaned-token")
            actual = state / "pats" / "default-recovered.token"
            actual.write_text("active-token")
            response = json.dumps({"pats": [{"name": "default", "token_file": str(actual)}]}).encode()
            with mock.patch.object(smoke_local.subprocess, "check_output", return_value=response) as cli:
                self.assertEqual(smoke_local.wait_for_default_token("xmesh", state), "active-token")
                self.assertIn("pats", cli.call_args.args[0])
                self.assertIn(str(state), cli.call_args.args[0])


class AcceptConfig(unittest.TestCase):
    def setUp(self):
        self.root = pathlib.Path(tempfile.mkdtemp())
        self.key = self.root / "key"
        self.key.write_text("tskey-auth-example")

    def config(self, out):
        return json.loads((self.root / out / "config.json").read_text())

    def test_lan_node(self):
        code, _, err = generate("lan", "--name", "lan-a", "--out", str(self.root / "a"))
        self.assertEqual(code, 0, err)
        config = self.config("a")
        self.assertEqual(config["mesh"], {"mode": "lan", "lan": {"mdns": True}})
        self.assertEqual(config["node"], {"name": "lan-a", "roles": ["agent", "gateway"]})
        self.assertEqual(config["agent"]["cwd_policy"]["allow"], [str(self.root / "a" / "work")])
        self.assertIn("tag:peer", config["agent"]["authz"])
        self.assertTrue((self.root / "a" / "state").is_dir())

    def test_gateway_pat_is_created_and_referenced(self):
        pat = self.root / "gw.pat"
        code, out, err = generate("lan", "--name", "pat-a", "--out", str(self.root / "p"), "--pat-file", str(pat))
        self.assertEqual(code, 0, err)
        self.assertEqual(self.config("p")["gateway"]["pats"]["accept"]["token_file"], str(pat))
        self.assertGreaterEqual(len(pat.read_text().strip()), 32)
        self.assertNotIn(pat.read_text().strip(), out)

    def test_tailnet_node_references_the_key_file_only(self):
        code, out, err = generate("tailnet", "--name", "dev-a", "--out", str(self.root / "t"), "--authkey-file", str(self.key))
        self.assertEqual(code, 0, err)
        config = self.config("t")
        self.assertEqual(config["mesh"]["authkey_file"], str(self.key))
        self.assertNotIn("tskey", json.dumps(config) + out)
        self.assertIn("tag:ingress", config["agent"]["authz"])

    def test_ingress_node_and_password(self):
        code, out, err = generate("ingress", "--name", "ing-a", "--out", str(self.root / "i"), "--authkey-file", str(self.key),
                                  "--issuer", "https://ing-a.tail1234.ts.net/", "--max-pty-per-client", "3",
                                  "--pty-budget-authority", "home-server")
        self.assertEqual(code, 0, err)
        config = self.config("i")
        self.assertEqual(config["node"]["roles"], ["agent", "gateway", "ingress"])
        ingress = config["ingress"]
        self.assertEqual((ingress["listen"], ingress["max_pty_per_client"], ingress["pty_budget_authority"]),
                         ("funnel", 3, "home-server"))
        self.assertEqual(ingress["oauth"]["issuer"], "https://ing-a.tail1234.ts.net")
        self.assertIn("pty:*", ingress["oauth"]["clients"]["check"]["scopes"])
        self.assertIn("exec:*", ingress["oauth"]["dynamic_client_scopes"])
        password = (self.root / "i" / "state" / "oauth-password")
        self.assertEqual(password.stat().st_mode & 0o777 if sys.platform != "win32" else 0o600, 0o600)
        self.assertNotIn(password.read_text().strip(), out)

    def test_existing_password_file_is_kept(self):
        password = self.root / "pw"
        password.write_text("keep-me")
        code, out, _ = generate("ingress", "--name", "ing-a", "--out", str(self.root / "i"), "--authkey-file", str(self.key),
                                "--issuer", "https://x.ts.net", "--password-file", str(password))
        self.assertEqual(code, 0)
        self.assertEqual(password.read_text(), "keep-me")
        self.assertIn("--oauth-password-file %s" % password.resolve(), json.loads(out)["start"])

    def test_public_listener_needs_a_domain(self):
        code, _, err = generate("ingress", "--name", "ing-a", "--out", str(self.root / "i"), "--authkey-file", str(self.key),
                                "--issuer", "https://mcp.example.com", "--listen", "public:443")
        self.assertNotEqual(code, 0)
        self.assertIn("--domain", err)
        code, _, err = generate("ingress", "--name", "ing-a", "--out", str(self.root / "j"), "--authkey-file", str(self.key),
                                "--issuer", "https://mcp.example.com", "--listen", "public:443", "--domain", "mcp.example.com")
        self.assertEqual(code, 0, err)
        ingress = self.config("j")["ingress"]
        self.assertEqual((ingress["domain"], ingress["tls"]), ("mcp.example.com", "autocert"))

    def test_rejects_bad_input_and_overwrite(self):
        for args in (("lan", "--name", "bad name"), ("tailnet", "--name", "ok"),
                     ("ingress", "--name", "ok", "--authkey-file", str(self.key), "--issuer", "http://insecure.example.com"),
                     ("ingress", "--name", "ok", "--authkey-file", str(self.key), "--issuer", "https://x.ts.net",
                      "--control-url", "https://hs.example.com")):
            code, _, err = generate(*args, "--out", str(self.root / "bad"))
            self.assertNotEqual(code, 0, args)
            self.assertIn("error:", err)
        self.assertEqual(generate("lan", "--name", "ok", "--out", str(self.root / "o"))[0], 0)
        self.assertNotEqual(generate("lan", "--name", "ok", "--out", str(self.root / "o"))[0], 0)
        self.assertEqual(generate("lan", "--name", "ok", "--out", str(self.root / "o"), "--force")[0], 0)


class AcceptCollect(unittest.TestCase):
    def test_redaction(self):
        text = accept_collect.redact('tskey-auth-kAbC123-xyz Bearer abcdefgh12345 "password": "hunter2hunter" authkey=abcdef1234')
        for secret in ("kAbC123", "abcdefgh12345", "hunter2hunter", "abcdef1234"):
            self.assertNotIn(secret, text)
        self.assertEqual(accept_collect.redact("devices.list ok"), "devices.list ok")

    def test_audit_keeps_names_and_status_only(self):
        state = pathlib.Path(tempfile.mkdtemp())
        (state / "audit").mkdir()
        rows = [{"time": "t%d" % i, "event": "tool_call", "tool": "exec.run", "device": "d", "status": "ok", "error": None,
                 "args_digest": {"cmd": "secret-command", "resolved_path": "/home/x"}, "output_sha256": "ab"} for i in range(5)]
        (state / "audit" / "2026-10-16.jsonl").write_text("\n".join(json.dumps(r) for r in rows) + "\nnot json\n")
        tail = accept_collect.audit_tail(state, 3)
        self.assertEqual([row["time"] for row in tail], ["t2", "t3", "t4"])
        self.assertNotIn("secret-command", json.dumps(tail))
        self.assertNotIn("output_sha256", tail[0])
        self.assertEqual(accept_collect.audit_tail(state / "missing", 3), [])


class FakeIngress(http.server.BaseHTTPRequestHandler):
    """An MCP endpoint that lets the first `limit` pty.open calls through and refuses the rest."""
    lock = threading.Lock()
    opened = []
    limit = 3

    def log_message(self, *args):
        pass

    def reply(self, body, session=None):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        if session:
            self.send_header("Mcp-Session-Id", session)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_DELETE(self):
        self.send_response(204)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert self.headers["Authorization"] == "Bearer token-1"
        method, params = request["method"], request.get("params", {})
        if "id" not in request:
            self.send_response(202)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        result = {}
        if method == "tools/call":
            name = params["name"]
            with FakeIngress.lock:
                if name.endswith("/pty.open") and len(FakeIngress.opened) < FakeIngress.limit:
                    sid = "p-%08d" % len(FakeIngress.opened)
                    FakeIngress.opened.append(sid)
                    result = {"structuredContent": {"session_id": sid}}
                elif name.endswith("/pty.open"):
                    result = {"isError": True, "structuredContent": {"error": "policy_denied", "rule": "quota.exceeded"}}
                elif name.endswith("/pty.list"):
                    result = {"structuredContent": {"sessions": [{"id": s, "state": "active"} for s in FakeIngress.opened]}}
                else:
                    result = {"structuredContent": {"state": "closed"}}
        self.reply({"jsonrpc": "2.0", "id": request["id"], "result": result}, session="s-1")


class PtyBudget(unittest.TestCase):
    def serve(self, limit):
        FakeIngress.opened, FakeIngress.limit = [], limit
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeIngress)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.shutdown)
        self.addCleanup(server.server_close)
        return "https://%s.example=127.0.0.1:%d" % ("a", server.server_address[1])

    def test_endpoint_specs(self):
        direct = accept_net.Endpoint("https://mcp.example.com/")
        self.assertEqual((direct.issuer, direct.base, direct.tunnel), ("https://mcp.example.com", "mcp.example.com", False))
        tunnel = accept_net.Endpoint("https://mcp.example.com=127.0.0.1:9000")
        self.assertEqual((tunnel.issuer, tunnel.base, tunnel.tunnel), ("https://mcp.example.com", "127.0.0.1:9000", True))
        for bad in ("http://mcp.example.com", "mcp.example.com", "https://"):
            with self.assertRaises(ValueError):
                accept_net.Endpoint(bad)

    def test_session_reports_tool_errors_without_raising(self):
        endpoint = accept_net.Endpoint(self.serve(0))
        session = accept_net.Session(endpoint, "token-1")
        ok, content = session.call("home/pty.open", {"cols": 80, "rows": 24})
        self.assertFalse(ok)
        self.assertEqual(accept_pty_budget.classify(ok, content), "quota.exceeded")
        session.close()

    def test_burst_over_two_ingresses_meets_the_limit(self):
        endpoint = accept_net.Endpoint(self.serve(3))
        endpoints = [endpoint, accept_net.Endpoint(endpoint.issuer.replace("a.", "b.") + "=" + endpoint.base)]
        tokens = {item.issuer: "token-1" for item in endpoints}
        outcomes = accept_pty_budget.burst(endpoints, tokens, "home", 10)
        names = [outcome[0] for outcome in outcomes]
        self.assertEqual((names.count("opened"), names.count("quota.exceeded")), (3, 7))
        ok, listing = outcomes[0][1].call("home/pty.list")
        ours = {outcome[2] for outcome in outcomes if outcome[2]}
        live = len([row for row in listing["sessions"] if row["id"] in ours])
        self.assertEqual(accept_pty_budget.verdict_limit(outcomes, 3, live), [])
        self.assertTrue(accept_pty_budget.verdict_limit(outcomes, 4, live))
        for outcome in outcomes:
            outcome[1].close()

    def test_verdict_names_each_problem(self):
        outcomes = [("opened", None, "p-1", {})] * 5 + [("quota.unavailable", None, None, {})]
        problems = accept_pty_budget.verdict_limit(outcomes, 3, 4)
        self.assertEqual(len(problems), 3)


if __name__ == "__main__":
    unittest.main()


class WindowsL2Refusal(unittest.TestCase):
    REASON = "当前账户无法读取 NTFS 变更日志，Windows L2 需要以管理员（提权）或 LocalSystem 服务运行节点"

    def refused(self, **patch):
        result = {"is_error": True, "exit_code": None, "error": "policy_denied", "rule": "sandbox.unavailable", "reason": self.REASON, "text": ""}
        return {**result, **patch}

    def test_explained_refusal_passes(self):
        warnings = f"Windows L2 不可用，L2 继续拒绝执行（sandbox.unavailable）：{self.REASON}"
        self.assertTrue(all(c["ok"] for c in accept_windows_l2.judge_refusal(warnings, self.refused())))

    def test_each_missing_piece_fails(self):
        for warnings, result in [
            ("", self.refused()),
            ("Windows L2 运行器未能启动（sandbox.unavailable）", self.refused()),
            (f"（sandbox.unavailable）：{self.REASON}", self.refused(reason=None)),
            (f"（sandbox.unavailable）：{self.REASON}", self.refused(error=None, rule=None, exit_code=0)),
        ]:
            self.assertFalse(all(c["ok"] for c in accept_windows_l2.judge_refusal(warnings, result)), (warnings, result))
