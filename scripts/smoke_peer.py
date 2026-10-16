#!/usr/bin/env python3
"""Verify file, exec and PTY calls to an existing peer through a loopback Gateway."""

import argparse
import json
import time
import uuid

from smoke_local import MCP, powershell


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", default="127.0.0.1:8080")
    parser.add_argument("--device", required=True)
    parser.add_argument("--expected-os", required=True, choices=["darwin", "linux", "windows"])
    options = parser.parse_args()
    print(json.dumps(verify_peer(options.gateway, options.device, options.expected_os)))


def verify_peer(gateway, device, expected_os):
    assert gateway.startswith("127.0.0.1:"), "Gateway must use loopback"
    assert device != "local", "Choose the peer node name"
    client = MCP(gateway)
    terminal = None
    created = False
    filename = "xmesh-peer-smoke-" + uuid.uuid4().hex
    call = lambda name, args=None: client.call(device + "/" + name, args)
    try:
        deadline = time.monotonic() + 20
        while True:
            devices = client.call("devices.list")["devices"]
            if any(row["id"] == device and row["online"] for row in devices):
                break
            assert time.monotonic() < deadline, devices
            time.sleep(0.25)
        local = client.call("local/sys.info")
        # A device is listed as online before the Gateway has fetched its capabilities.
        deadline = time.monotonic() + 30
        while True:
            result = client.request("tools/call", {"name": device + "/sys.info", "arguments": {}})
            if not result.get("isError"):
                peer = result["structuredContent"]
                break
            assert (result.get("structuredContent") or {}).get("error") == "not_found" and time.monotonic() < deadline, result
            time.sleep(0.5)
        assert peer["hostname"] == device and peer["os"] == expected_os, peer
        assert peer["network"]["mode"] == "tailnet", peer["network"]
        assert not call("fs.stat", {"path": filename})["exists"]
        call("fs.write", {"path": filename, "content": "XMESH_PEER_FILE"})
        created = True
        assert "XMESH_PEER_FILE" in call("fs.read", {"path": filename})["content"]
        windows = expected_os == "windows"
        executed = call("exec.run", powershell("[Console]::Out.Write('XMESH_PEER_EXEC')") if windows
                        else {"cmd": "sh", "args": ["-c", "printf XMESH_PEER_EXEC"]})
        assert executed["exit_code"] == 0 and executed["stdout"] == "XMESH_PEER_EXEC", executed
        terminal = call("pty.open", {"cols": 80, "rows": 24})
        assert terminal["owner"].startswith(local["network"]["node_id"] + "/"), terminal
        sid = terminal["session_id"]
        call("pty.write", {"session_id": sid, "data": "Write-Output ('XMESH_PEER_' + 'PTY')\r" if windows
                           else "printf 'XMESH_PEER_%s\\n' PTY\n"})
        output, cursor = "", 0
        deadline = time.monotonic() + 10
        while "XMESH_PEER_PTY" not in output and time.monotonic() < deadline:
            page = call("pty.read", {"session_id": sid, "cursor": cursor, "wait_ms": 100})
            output += page["data"]
            cursor = page["next_cursor"]
        assert "XMESH_PEER_PTY" in output, output
        assert call("pty.close", {"session_id": sid})["state"] == "closed"
        terminal = None
        assert call("fs.delete", {"path": filename})["removed"] == 1
        created = False
        assert not call("fs.stat", {"path": filename})["exists"]
        client.close()
        return {"result": "PASS", "from": local["hostname"], "to": peer["hostname"],
                "os": peer["os"], "caller_node": local["network"]["node_id"],
                "checks": ["discovery", "identity", "file", "exec", "pty", "cleanup"]}
    finally:
        try:
            if terminal:
                call("pty.close", {"session_id": terminal["session_id"]})
        finally:
            try:
                if created:
                    call("fs.delete", {"path": filename})
            finally:
                client.close()


if __name__ == "__main__":
    main()
