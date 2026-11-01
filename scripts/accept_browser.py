#!/usr/bin/env python3
"""Real-environment acceptance for the agent-browser backend (docs/validation/agent-browser.md).

Starts a local xmesh node whose `browser` backend runs packages/agent-browser through the
Gateway, serves a small fixture page on 127.0.0.1, and checks, with two separate callers:

  * open / snapshot / fill / click / screenshot work, and the screenshot arrives as an image block;
  * the two callers get separate browsers (neither sees the other's page);
  * a host outside AGENT_BROWSER_ALLOWED_DOMAINS (`localhost`) is refused;
  * `evaluate` is denied by the action policy;
  * the page text and the typed value never reach the audit log;
  * after the callers leave and the node stops, no namespace directory or daemon remains.

Needs Node.js 20+, a Chrome that agent-browser can launch, and the package installed:

    node packages/agent-browser/install.mjs --prefix /tmp/xmesh-browser
    python3 scripts/accept_browser.py --binary ./xmesh --prefix /tmp/xmesh-browser
"""

# conformance: CON-TOOL-10 CON-TOOL-12
import argparse
import base64
import http.server
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import threading
import time

from smoke_local import MCP, address, stop_runtime, wait_for_gateway

SECRET = "ACCEPT-TYPED-SECRET-7731"
PAGE = """<!doctype html><title>{name}</title><h1>caller {name}</h1>
<label>Query<input aria-label="Query"></label><button onclick="document.querySelector('output').textContent='Found '+document.querySelector('input').value">Search</button><output></output>
<p>PAGE-BODY-MARKER-{name}</p>"""


class Fixture(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = PAGE.format(name=self.path.strip("/") or "root").encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def namespaces():
    root = pathlib.Path.home() / ".agent-browser" / "namespaces"
    return {item.name for item in root.iterdir() if item.name.startswith("xmesh-")} if root.is_dir() else set()


def live_daemons(names):
    """Namespaces with a running (non-zombie) process; Linux only, other systems rely on the directory check."""
    if not sys.platform.startswith("linux") or not names:
        return []
    found = []
    for proc in pathlib.Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            if (proc / "stat").read_text().rsplit(")", 1)[1].split()[0] == "Z":
                continue
            environ = (proc / "environ").read_bytes().split(b"\0")
        except (OSError, IndexError):
            continue
        for item in environ:
            if item.startswith(b"AGENT_BROWSER_NAMESPACE=") and item.split(b"=", 1)[1].decode() in names:
                found.append(int(proc.name))
    return found


def tool(session, name, arguments=None):
    return session.request("tools/call", {"name": "local/browser." + name, "arguments": arguments or {}})


def text_of(result):
    return " ".join(block.get("text", "") for block in result.get("content", []))


def ok(result):
    assert not result.get("isError"), result
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--prefix", required=True, type=pathlib.Path, help="directory given to install.mjs")
    options = parser.parse_args()
    binary, prefix = options.binary.resolve(), options.prefix.resolve()
    launcher = prefix / "bin" / "xmesh-agent-browser.mjs"
    assert launcher.is_file(), "run packages/agent-browser/install.mjs first"

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    before = namespaces()
    seen_namespaces = set()

    with tempfile.TemporaryDirectory(prefix="xmesh-browser-accept-") as temporary:
        root = pathlib.Path(temporary).resolve()
        work = root / "work"
        work.mkdir()
        state = root / "state"
        state.mkdir()
        tokens = {}
        pats = {}
        for who in ("alice", "bob"):
            tokens[who] = f"{who}-" + base64.urlsafe_b64encode(os.urandom(24)).decode()
            file = root / f"{who}.pat"
            file.write_text(tokens[who])
            file.chmod(0o600)
            pats[who] = {"token_file": str(file), "scopes": ["devices:read", "backend:*:browser"]}
        direct, gateway = address(), address()
        cfg = {"schema_version": 2, "node": {"name": "browser-accept", "roles": ["agent", "gateway"]},
               "mesh": {"lan": {"enabled": True, "mdns": True}, "tailnet": {"enabled": False}},
               "gateway": {"listen": gateway, "pats": pats},
               "agent": {"default_cwd": str(work), "cwd_policy": {"allow": [str(work)]},
                         "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}},
                         "backends": {"browser": {
                             "transport": "stdio", "policy": "per-caller", "idle_timeout": "10m",
                             "shutdown_timeout": "15s", "process_cleanup": "tree", "cwd": str(work),
                             "command": ["node", str(launcher), "--tools", "core,tabs"]}}}}
        config = root / "config.json"
        config.write_text(json.dumps(cfg))
        (state / "backend-env.json").write_text(json.dumps(
            {"backends": {"browser": {"AGENT_BROWSER_ALLOWED_DOMAINS": "127.0.0.1"}}}))
        (state / "backend-env.json").chmod(0o600)
        with (root / "runtime.log").open("w+") as log:
            process = subprocess.Popen([str(binary), "serve", "--local", "--config", str(config),
                                        "--state-dir", str(state), "--listen", direct], stdout=log, stderr=log)
            try:
                alice = wait_for_gateway(process, gateway, log, tokens["alice"])
                bob = MCP(gateway, tokens["bob"])
                names = lambda s: {t["name"] for t in s.request("tools/list", {})["tools"]}
                deadline = time.monotonic() + 30
                while "local/browser.agent_browser_open" not in names(alice):
                    if time.monotonic() >= deadline:
                        log.seek(0)
                        raise AssertionError("browser tools never appeared: %s\n%s" % (sorted(names(alice)), log.read()[-3000:]))
                    time.sleep(0.3)
                assert "local/browser.agent_browser_snapshot" in names(bob)

                # Open pages for two callers in turn; each must see only its own page.
                for who, session in (("a", alice), ("b", bob)):
                    ok(tool(session, "agent_browser_open", {"url": f"{base}/{who}"}))
                refs = {}
                for who, session, other in (("a", alice, "b"), ("b", bob, "a")):
                    snap = text_of(ok(tool(session, "agent_browser_snapshot", {"interactive": True})))
                    assert 'textbox "Query"' in snap and 'button "Search"' in snap, snap
                    refs[who] = {kind: re.search(kind + r'[^\n]*\[ref=(e\d+)\]', snap).group(1) for kind in ("textbox", "button")}
                    heading = text_of(ok(tool(session, "agent_browser_get_text", {"selector": "h1"})))
                    assert f"caller {who}" in heading and f"caller {other}" not in heading, heading

                ok(tool(alice, "agent_browser_fill", {"selector": "@" + refs["a"]["textbox"], "text": SECRET}))
                ok(tool(alice, "agent_browser_click", {"selector": "@" + refs["a"]["button"]}))
                found = text_of(ok(tool(alice, "agent_browser_get_text", {"selector": "output"})))
                assert SECRET in found, found
                assert SECRET not in text_of(ok(tool(bob, "agent_browser_get_text", {"selector": "output"}))), "callers share a browser"

                shot = ok(tool(alice, "agent_browser_screenshot", {}))
                images = [b for b in shot["content"] if b.get("type") == "image"]
                assert images and base64.b64decode(images[0]["data"])[:4] == b"\x89PNG", shot["content"]

                # Safe defaults.
                outside = tool(alice, "agent_browser_open", {"url": f"http://localhost:{server.server_port}/"})
                assert outside.get("isError") and "allowed domains" in text_of(outside), outside
                denied = tool(alice, "agent_browser_eval", {"script": "1+1"})
                assert denied.get("isError") and "denied by policy" in text_of(denied), denied
                assert "local/browser.agent_browser_download" not in names(alice)

                seen_namespaces = namespaces() - before
                assert len(seen_namespaces) == 2, ("expected one namespace per caller", seen_namespaces)
                alice.close()
                bob.close()
            finally:
                stop_runtime(process)

        # Audit: tool names and domains may appear; page text and typed values must not.
        audit = "".join(p.read_text(errors="replace") for p in state.rglob("*.jsonl"))
        assert audit, "no audit records were written"
        for marker in (SECRET, "PAGE-BODY-MARKER"):
            assert marker not in audit, f"{marker} reached the audit log"

    # Cleanup: namespace directories and daemons must be gone shortly after the node stops.
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        left = namespaces() & seen_namespaces
        alive = live_daemons(seen_namespaces)
        if not left and not alive:
            break
        time.sleep(0.5)
    assert not left, f"namespace directories remain: {left}"
    assert not alive, f"browser daemons remain: {alive}"
    server.shutdown()
    print(json.dumps({"result": "PASS", "checks": [
        "tools-listed", "separate-browsers", "fill-click-read", "screenshot-image", "domain-allowlist",
        "evaluate-denied", "audit-clean", "daemon-reclaimed"]}))


if __name__ == "__main__":
    main()
