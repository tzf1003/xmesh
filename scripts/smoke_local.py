#!/usr/bin/env python3
"""Exercise a built xmesh Agent and Gateway on loopback using MCP over HTTP."""

import argparse
import base64
import hashlib
import http.client
import json
import pathlib
import secrets
import shlex
import socket
import subprocess
import sys
import time
import tempfile
import time


from file_transfer import upload as upload_file, download as download_file


WINDOWS = sys.platform == "win32"


def powershell(script):
    """The exec.run arguments that run a PowerShell script, for hosts without sh.

    Windows PowerShell is always installed; pwsh may be only a Store alias, which exec refuses.
    """
    return {"cmd": "powershell", "args": ["-NoProfile", "-NonInteractive", "-Command", script]}


def address():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{sock.getsockname()[1]}"


class MCP:
    def __init__(self, addr, token=None, host=None, timeout=15):
        self.addr, self.session, self.counter = addr, None, 0
        self.token, self.host, self.timeout = token, host, timeout
        self.request("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                    "clientInfo": {"name": "xmesh-smoke", "version": "1"}})
        self.request("notifications/initialized", {}, notification=True)

    def request(self, method, params, notification=False):
        self.counter += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = self.counter
        headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": "2025-11-25"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        if self.host:
            headers["Host"] = self.host
        connection = http.client.HTTPConnection(self.addr, timeout=self.timeout)
        try:
            connection.request("POST", "/mcp", json.dumps(body), headers)
            response = connection.getresponse()
            data = response.read().decode()
            assert response.status in (200, 202), (response.status, data)
            self.session = response.getheader("Mcp-Session-Id") or self.session
            if notification:
                return None
            if response.getheader("Content-Type", "").startswith("text/event-stream"):
                messages = [json.loads(line[6:]) for line in data.splitlines() if line.startswith("data: ")]
                result = next(item for item in messages if item.get("id") == self.counter)
            else:
                result = json.loads(data)
            assert "error" not in result, result
            return result["result"]
        finally:
            connection.close()

    def call(self, name, args=None):
        result = self.request("tools/call", {"name": name, "arguments": args or {}})
        assert not result.get("isError"), result
        return result["structuredContent"]

    def close(self):
        if not self.session:
            return
        connection = http.client.HTTPConnection(self.addr, timeout=self.timeout)
        try:
            headers = {"Mcp-Session-Id": self.session, "MCP-Protocol-Version": "2025-11-25"}
            if self.token:
                headers["Authorization"] = "Bearer " + self.token
            if self.host:
                headers["Host"] = self.host
            connection.request("DELETE", "/mcp", headers=headers)
            response = connection.getresponse()
            response.read()
            assert response.status in (200, 204), response.status
            self.session = None
        finally:
            connection.close()


def wait_for_default_token(binary, state, seconds=15):
    """Read the default PAT's actual file path after the kernel publishes its configuration."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            raw = subprocess.check_output(
                [str(binary), "control", "pats", "list", "--state-dir", str(state)],
                stderr=subprocess.DEVNULL, timeout=3)
            pats = json.loads(raw)["pats"]
            entry = next(pat for pat in pats if pat["name"] == "default")
            token = pathlib.Path(entry["token_file"]).read_text().strip()
            if token:
                return token
        except (OSError, subprocess.SubprocessError, ValueError, KeyError, StopIteration):
            pass
        time.sleep(0.1)
    raise AssertionError("the gateway did not publish its default token")


def wait_for_gateway(process, gateway, log, token=None, seconds=15, host=None):
    """Return an MCP session once the Gateway answers, or raise with the runtime log."""
    deadline = time.monotonic() + seconds
    while True:
        try:
            return MCP(gateway, token, host)
        except (OSError, http.client.HTTPException):
            if process.poll() is not None or time.monotonic() >= deadline:
                log.seek(0)
                raise RuntimeError(log.read())
            time.sleep(0.05)


def stop_runtime(process, windows=sys.platform == "win32"):
    """Stop the runtime and require a clean exit. Windows has no SIGTERM, so any exit code passes."""
    process.terminate()
    try:
        process.wait(timeout=15)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
        raise AssertionError("runtime shutdown exceeded 15s")
    assert windows or process.returncode == 0, process.returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--isolation", choices=["L1", "L2"], default="L1")
    parser.add_argument("--shell", default="auto" if WINDOWS else "/bin/sh", help="Agent PTY shell (or auto)")
    parser.add_argument("--pat", action="store_true", help="Require a temporary Gateway PAT")
    options = parser.parse_args()
    assert not (WINDOWS and options.isolation == "L2"), "L2 is not available through this script on Windows"
    binary = options.binary.resolve()
    with tempfile.TemporaryDirectory(prefix="xmesh-smoke-") as temporary:
        root = pathlib.Path(temporary).resolve()
        workspace = root / "workspace"
        workspace.mkdir()
        outside = root / "host-only"
        outside.write_text("host-only")
        direct, gateway = address(), address()
        cfg = {"schema_version": 2, "node": {"name": "smoke-node", "roles": ["agent", "gateway"]},
               "mesh": {"lan": {"enabled": True, "mdns": True}, "tailnet": {"enabled": False}}, "gateway": {"listen": gateway},
               "agent": {"default_cwd": str(workspace), "cwd_policy": {"allow": [str(workspace)]},
                         "policy": {"isolation": options.isolation},
                         "pty": {"default_shell": options.shell},
                         "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}}}}
        token = None
        if options.pat:
            token = secrets.token_urlsafe(32)
            pat_file = root / "gateway-pat"
            pat_file.touch(mode=0o600)
            pat_file.write_text(token)
            cfg["gateway"]["pats"] = {"smoke": {"token_file": str(pat_file),
                "scopes": ["devices:read", "sys:*", "fs:*", "exec:*", "pty:*"]}}
        config = root / "config.json"
        config.write_text(json.dumps(cfg))
        state = root / "state"
        with (root / "runtime.log").open("w+") as log:
            process = subprocess.Popen([str(binary), "serve", "--local", "--config", str(config),
                                        "--state-dir", str(state), "--listen", direct], stdout=log, stderr=log)
            try:
                if not options.pat:
                    token = wait_for_default_token(binary, state)
                client = wait_for_gateway(process, gateway, log, token)
                status = subprocess.check_output([str(binary), "status", "--endpoint", f"http://{direct}/mcp", "--json"])
                info = json.loads(status)
                assert info["roles"] == ["agent", "gateway"] and info["network"]["mode"] == "local", info
                if options.pat:
                    status = subprocess.check_output([str(binary), "status", "--endpoint", f"http://{gateway}/mcp",
                        "--tool", "local/sys.info", "--pat-file", str(pat_file), "--json"])
                    assert json.loads(status)["hostname"] == "smoke-node"
                tools = client.request("tools/list", {})["tools"]
                assert any(tool["name"] == "local/exec.run" for tool in tools)
                client.call("local/fs.write", {"path": "roundtrip", "content": "SMOKE_PRIVATE_BODY"})
                assert "SMOKE_PRIVATE_BODY" in client.call("local/fs.read", {"path": "roundtrip"})["content"]
                payload = bytes(range(256)) + b"\xef\xbb\xbfhello\r\n"
                encoded = base64.b64encode(payload).decode()
                upload = {"path": "transfer.bin", "data": encoded, "sha256": hashlib.sha256(payload).hexdigest()}
                saved = client.call("local/fs.upload", upload)
                assert saved["sha256"] == upload["sha256"] and saved["bytes"] == len(payload)
                downloaded = client.call("local/fs.download", {"path": upload["path"]})
                assert base64.b64decode(downloaded["data"]) == payload
                assert downloaded["sha256"] == saved["sha256"]
                conflict = client.request("tools/call", {"name": "local/fs.upload", "arguments": upload})
                assert conflict.get("isError") and conflict["structuredContent"]["error"] == "conflict"
                changed = b"replacement\x00\xff"
                client.call("local/fs.upload", {"path": upload["path"], "data": base64.b64encode(changed).decode(),
                    "sha256": hashlib.sha256(changed).hexdigest(), "force": True})
                assert (workspace / upload["path"]).read_bytes() == changed
                source, restored = root / "source.bin", root / "restored.bin"
                source.write_bytes(bytes(range(256)) * 8193)
                upload_file(client, "local/fs.upload", source, "chunked.bin", False)
                download_file(client, "local/fs.download", "chunked.bin", restored, False)
                assert hashlib.sha256(source.read_bytes()).digest() == hashlib.sha256(restored.read_bytes()).digest()
                script = 'printf SMOKE_EXEC_MARKER'
                if options.isolation == "L2":
                    home_check = 'test "$HOME" = "$PWD"' if sys.platform == "darwin" else 'test "$HOME" = /tmp'
                    script = home_check + ' && test ! -r ' + shlex.quote(str(outside)) + ' && ' + script
                if WINDOWS:
                    # Write() adds no newline, matching printf.
                    run = powershell("[Console]::Out.Write('SMOKE_EXEC_MARKER')")
                else:
                    run = {"cmd": "sh", "args": ["-c", script]}
                executed = client.call("local/exec.run", run)
                assert executed["exit_code"] == 0 and executed["stdout"] == "SMOKE_EXEC_MARKER", executed
                terminal = client.call("local/pty.open", {"cols": 80, "rows": 24})
                sid, owner = terminal["session_id"], terminal["owner"]
                assert owner.startswith("local/") and len(owner) > len("local/")
                terminal_command = ("Write-Output ('SMOKE_' + 'PTY_MARKER')" if WINDOWS
                                    else "printf 'SMOKE_%s\\n' PTY_MARKER")
                if options.isolation == "L2":
                    terminal_command = ('if test ! -r ' + shlex.quote(str(outside)) +
                        '; then (printf blocked > ' + shlex.quote(str(outside)) +
                        ') 2>/dev/null; ' + terminal_command + '; fi')
                newline = "\r" if WINDOWS else "\n"
                client.call("local/pty.write", {"session_id": sid, "data": terminal_command + newline})
                text, cursor = "", 0
                deadline = time.monotonic() + 5
                while "SMOKE_PTY_MARKER" not in text and time.monotonic() < deadline:
                    output = client.call("local/pty.read", {"session_id": sid, "cursor": cursor, "wait_ms": 100})
                    text += output["data"]
                    cursor = output["next_cursor"]
                assert "SMOKE_PTY_MARKER" in text, text
                assert "job control turned off" not in text, text
                client.call("local/pty.resize", {"session_id": sid, "cols": 101, "rows": 37})
                size_command = ("Write-Output ('SMOKE_SIZE=' + $Host.UI.RawUI.WindowSize.Height + ' ' + "
                                "$Host.UI.RawUI.WindowSize.Width)" if WINDOWS else "printf 'SMOKE_SIZE='; stty size")
                client.call("local/pty.write", {"session_id": sid, "data": size_command + newline})
                resized = ""
                deadline = time.monotonic() + 5
                while "SMOKE_SIZE=37 101" not in resized and time.monotonic() < deadline:
                    output = client.call("local/pty.read", {"session_id": sid, "cursor": cursor, "wait_ms": 100})
                    resized += output["data"]
                    cursor = output["next_cursor"]
                assert "SMOKE_SIZE=37 101" in resized, resized
                assert client.call("local/pty.close", {"session_id": sid})["state"] == "closed"
                assert outside.read_text() == "host-only", "sandbox changed host file"
                client.close()
            finally:
                stop_runtime(process)
            audit = "".join(path.read_text() for path in (state / "audit").glob("*.jsonl"))
            assert audit and all(marker not in audit for marker in ["SMOKE_PRIVATE_BODY", "SMOKE_EXEC_MARKER", "SMOKE_PTY_MARKER"])
            print(json.dumps({"result": "PASS", "isolation": options.isolation, "pat": options.pat, "tools": len(tools),
                              "checks": ["status", "gateway", "file-roundtrip", "exec", "pty", "shutdown", "audit"]}))


if __name__ == "__main__":
    main()
